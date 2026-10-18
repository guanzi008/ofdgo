// Copyright 2025-2026 肖其顿 (XIAO QI DUN)
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ofdgo

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"image/color"
	"math"
	"strconv"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfPointBounds 逐点汇总边界，不保留文字或路径的临时坐标数组
type pdfPointBounds struct {
	minX, minY, maxX, maxY float64
	found                  bool
}

// pdfGeometryPath 转换PDF路径并衔接OFD几何类型
// 入参: ctx 取消上下文, source PDF路径, matrix 坐标变换
// 返回: GeometryPath 独立几何路径, error 指令、坐标或取消错误
func pdfGeometryPath(ctx context.Context, source pdfgo.Path, matrix pdfgo.Matrix) (GeometryPath, error) {
	path, err := codecpdf.GeometryPath(ctx, source, matrix)
	return GeometryPath(path), err
}

// compileCoverage 直接编译无文字裁剪的填充覆盖，其他语义交给配置的页面编译器
// 入参: node 图元, stroke 是否描边
// 返回: *RasterPage 覆盖场景, Box 页面区域, error 编译错误
func (p *pdfImporter) compileCoverage(node pdfCompositeNode, stroke bool) (*RasterPage, Box, error) {
	fallback := func() (*RasterPage, Box, error) {
		return p.compileGroup(func(local *pdfImporter) error { return node.coverage(local, stroke) })
	}
	if stroke || node.path == nil && node.image == nil {
		return fallback()
	}
	deferredClip := false
	switch p.editor.Backends().Compiler.(type) {
	case CanvasBackend, *CanvasBackend:
	case OFDCompiler, *OFDCompiler:
		deferredClip = true
	default:
		return fallback()
	}
	style, _, _ := node.style()
	for _, clip := range style.Clips {
		if len(clip.Text) != 0 {
			return fallback()
		}
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return nil, Box{}, err
	}
	switch geometry.(type) {
	case CanvasBackend, *CanvasBackend:
	default:
		return fallback()
	}
	var path GeometryPath
	evenOdd := false
	if node.path != nil {
		path, err = pdfGeometryPath(p.ctx, node.path.Path, p.matrix)
		evenOdd = node.path.Path.EvenOdd
	} else {
		path, err = geometry.Transform(GeometryPath(basegeom.Rectangle(Box{W: 1, H: 1})), MatrixFromValues([6]float64(p.matrix.Mul(node.image.Matrix))))
	}
	if err != nil {
		return nil, Box{}, err
	}
	if len(path) == 0 {
		return nil, Box{}, fmt.Errorf("empty PDF painted path")
	}
	path = GeometryPath(basegeom.Close(basegeom.Path(path)))
	clip, err := p.coverageClip(style.Clips, geometry)
	if err != nil || len(clip) == 0 {
		return nil, Box{}, err
	}
	compiler := semanticCompiler{geometry: geometry, page: &RasterPage{Width: p.pageWidth, Height: p.pageHeight, DPI: p.rasterDPI}}
	var bounds pdfPointBounds
	for _, segment := range path {
		if segment.Verb != GeometryClose {
			bounds.add(pdfgo.Point{X: segment.End.X, Y: segment.End.Y})
		}
		if segment.Verb == GeometryCubic {
			bounds.add(pdfgo.Point{X: segment.Control1.X, Y: segment.Control1.Y})
			bounds.add(pdfgo.Point{X: segment.Control2.X, Y: segment.Control2.Y})
		}
	}
	boundary := GeometryPath(basegeom.Rectangle(bounds.box()))
	clip, err = clipGeometry(geometry, clip, &boundary)
	if err != nil {
		return nil, Box{}, err
	}
	if deferredClip {
		if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, &clip, IdentityMatrix, nil); err != nil {
			return nil, Box{}, err
		}
		return p.groupSceneBounds(compiler.page)
	}
	box, err := geometry.Bounds(path)
	if err != nil {
		return nil, Box{}, err
	}
	rect, rectangle := basegeom.RectangleBounds(basegeom.Path(clip))
	if !rectangle || box.X < rect.X || box.Y < rect.Y || box.X+box.W > rect.X+rect.W || box.Y+box.H > rect.Y+rect.H {
		if evenOdd {
			path, err = geometry.Normalize(path, true)
			if err != nil {
				return nil, Box{}, err
			}
			evenOdd = false
		}
		var clipped GeometryPath
		var linear bool
		var clipErr error
		if rectangle {
			clipped, linear, clipErr = clipLinearFillGeometry(path, clip)
		}
		if clipErr != nil {
			return nil, Box{}, clipErr
		}
		if linear {
			path = clipped
		} else {
			path, err = clipGeometry(geometry, path, &clip)
			if err != nil {
				return nil, Box{}, err
			}
		}
	}
	if err := compiler.command(path, Paint{Color: color.RGBA{255, 255, 255, 255}}, evenOdd, nil, IdentityMatrix, nil); err != nil {
		return nil, Box{}, err
	}
	return p.groupSceneBounds(compiler.page)
}

// coverageClip 按完整路径、变换和页面范围复用只读裁剪，不缓存失败结果
// 入参: paths 无文字裁剪的路径集合, geometry 几何后端
// 返回: GeometryPath 共享只读轮廓, error 几何或取消错误
func (p *pdfImporter) coverageClip(paths []pdfgo.Path, geometry GeometryBackend) (GeometryPath, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	clip := GeometryPath(basegeom.Rectangle(Box{W: p.pageWidth, H: p.pageHeight}))
	if len(paths) == 0 {
		return clip, nil
	}
	hash := sha256.New()
	var size [16]byte
	binary.LittleEndian.PutUint64(size[:8], math.Float64bits(p.pageWidth))
	binary.LittleEndian.PutUint64(size[8:], math.Float64bits(p.pageHeight))
	hash.Write(size[:])
	for _, path := range paths {
		key, err := codecpdf.ClipPathKey(p.ctx, path, p.matrix)
		if err != nil {
			return nil, err
		}
		hash.Write(key[:])
	}
	var key [32]byte
	hash.Sum(key[:0])
	cache := p.compositingCache().clipCache()
	if cached, ok := cache.get(key); ok {
		return cached, nil
	}
	for _, source := range paths {
		part, err := pdfGeometryPath(p.ctx, source, p.matrix)
		if err != nil {
			return nil, err
		}
		part = GeometryPath(basegeom.Close(basegeom.Path(part)))
		if _, rectangle := basegeom.RectangleBounds(basegeom.Path(part)); source.EvenOdd && !rectangle {
			part, err = geometry.Normalize(part, true)
			if err != nil {
				return nil, err
			}
		}
		clip, err = clipGeometry(geometry, part, &clip)
		if err != nil {
			return nil, err
		}
		if len(clip) == 0 {
			break
		}
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if len(clip) <= (cache.limit-128)/112 {
		cache.put(key, clip, 128+len(clip)*112)
	}
	return clip, nil
}

// pdfGlyphPath 将OFD字形几何交给PDF路径适配器转换
// 入参: outline 字形轮廓, matrix 字形到页面的变换
// 返回: pdfgo.Path 页面路径, error 非字体轮廓指令
func pdfGlyphPath(outline GeometryPath, matrix pdfgo.Matrix) (pdfgo.Path, error) {
	return codecpdf.GlyphPath(basegeom.Path(outline), matrix)
}

// pdfNumbers 按OFD数值格式序列化坐标
// 入参: values 数值列表
// 返回: string 空格分隔的数值
func pdfNumbers(values ...float64) string {
	var buffer [128]byte
	data := buffer[:0]
	for n, v := range values {
		if n != 0 {
			data = append(data, ' ')
		}
		data = strconv.AppendFloat(data, v, 'g', -1, 64)
	}
	return string(data)
}

// pdfDashPattern 换算PDF虚线长度，将单值数组展开为等长线段与间隔
// 入参: dash PDF虚线数组, scale 坐标缩放比例
// 返回: string OFD虚线数组
func pdfDashPattern(dash []float64, scale float64) string {
	if len(dash) == 0 {
		return ""
	}
	values := make([]float64, max(2, len(dash)))
	for i, value := range dash {
		values[i] = value * scale
	}
	if len(dash) == 1 {
		values[1] = values[0]
	}
	return pdfNumbers(values...)
}

// pdfBoundary 序列化对象边界
// 入参: b 对象边界
// 返回: string OFD边界数据
func pdfBoundary(b Box) string { return pdfNumbers(b.X, b.Y, b.W, b.H) }

// pdfBounds 计算点集合的包围框
// 入参: points 坐标点集合
// 返回: Box 包围框
func pdfBounds(points []pdfgo.Point) Box {
	var bounds pdfPointBounds
	for _, point := range points {
		bounds.add(point)
	}
	return bounds.box()
}

// add 按原顺序累计一个控制点的最小和最大坐标
// 入参: point 坐标点
func (b *pdfPointBounds) add(point pdfgo.Point) {
	if !b.found {
		b.minX, b.minY, b.maxX, b.maxY = point.X, point.Y, point.X, point.Y
		b.found = true
	}
	b.minX, b.minY = math.Min(b.minX, point.X), math.Min(b.minY, point.Y)
	b.maxX, b.maxY = math.Max(b.maxX, point.X), math.Max(b.maxY, point.Y)
}

// box 取得原精度边界，空集合仍返回零值
// 返回: Box 坐标范围
func (b *pdfPointBounds) box() Box {
	if !b.found {
		return Box{}
	}
	return Box{X: b.minX, Y: b.minY, W: math.Max(b.maxX-b.minX, 1e-6), H: math.Max(b.maxY-b.minY, 1e-6)}
}

// pathBoundsContext 逐段计算变换后的控制点边界，不创建临时点集合
// 入参: ctx 取消上下文, path PDF路径
// 返回: Box 控制点边界，空路径返回零值, error 取消错误
func (p *pdfImporter) pathBoundsContext(ctx context.Context, path pdfgo.Path) (Box, error) {
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	found := false
	for _, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return Box{}, err
		}
		for index, point := range segment.Points {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return Box{}, err
				}
			}
			point = p.matrix.Apply(point)
			minX, minY = math.Min(minX, point.X), math.Min(minY, point.Y)
			maxX, maxY = math.Max(maxX, point.X), math.Max(maxY, point.Y)
			found = true
		}
	}
	if err := ctx.Err(); err != nil {
		return Box{}, err
	}
	if !found {
		return Box{}, nil
	}
	return Box{X: minX, Y: minY, W: math.Max(maxX-minX, 1e-6), H: math.Max(maxY-minY, 1e-6)}, nil
}

// pathDataContext 转换路径并在分段处理时检查取消
// 入参: ctx 取消上下文, path PDF路径, origin 对象边界
// 返回: string OFD路径数据, error 取消错误
func (p *pdfImporter) pathDataContext(ctx context.Context, path pdfgo.Path, origin Box) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	data := make([]byte, 0, len(path.Segments)*48)
	for index, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if index != 0 {
			data = append(data, ' ')
		}
		data = append(data, segment.Operator...)
		for _, point := range segment.Points {
			point = p.matrix.Apply(point)
			data = append(data, ' ')
			data = strconv.AppendFloat(data, point.X-origin.X, 'g', -1, 64)
			data = append(data, ' ')
			data = strconv.AppendFloat(data, point.Y-origin.Y, 'g', -1, 64)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(data), nil
}

// path 保留每次路径绘制操作，重叠描边不合并为单次绘制
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) path(mark pdfgo.PathMark) error {
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "path blend mode " + string(mark.Style.BlendMode)}
	}
	return p.appendPath(mark)
}

// appendPath 添加独立可编辑路径
// 入参: mark PDF路径绘制信息
// 返回: error 错误信息
func (p *pdfImporter) appendPath(mark pdfgo.PathMark) error {
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Path(mark) })
		}
		return err
	}
	if mark.Fill && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || mark.Stroke && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "color separation overprint"}
	}
	if p.warning != nil && (mark.Fill && p.gradientError(mark.Style.Fill) != nil || mark.Stroke && p.gradientError(mark.Style.Stroke) != nil) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		return p.gradientPath(mark)
	}
	box, err := p.pathBoundsContext(p.ctx, mark.Path)
	if err != nil {
		return err
	}
	if box == (Box{}) {
		return fmt.Errorf("empty PDF painted path")
	}
	scale := math.Hypot(p.matrix[0], p.matrix[1])
	drawing, origin, ctm := *p, box, ""
	marginScale := scale
	if mark.StrokeMatrix != (pdfgo.Matrix{}) {
		inverse, ok := mark.StrokeMatrix.Inverse()
		if !ok {
			return fmt.Errorf("invalid PDF stroke matrix")
		}
		drawing.matrix, origin, scale = inverse, Box{}, 1
		m := p.matrix.Mul(mark.StrokeMatrix)
		marginScale = math.Max(math.Hypot(m[0], m[2]), math.Hypot(m[1], m[3]))
	}
	if mark.Stroke {
		margin := mark.Style.LineWidth * marginScale / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{X: box.X - margin, Y: box.Y - margin, W: box.W + 2*margin, H: box.H + 2*margin}
	}
	if mark.StrokeMatrix == (pdfgo.Matrix{}) {
		origin = box
	} else {
		m := p.matrix.Mul(mark.StrokeMatrix)
		m[4], m[5] = m[4]-box.X, m[5]-box.Y
		ctm = pdfNumbers(m[:]...)
	}
	paint := func(value pdfgo.Paint) (*FillColor, error) {
		if value.Tiling == nil && value.Axial == nil && value.Radial == nil && value.Mesh == nil && value.Function == nil && value.Shading == nil {
			return p.color(value, mark.Style.RenderingIntent)
		}
		if value.Tiling != nil || pdfGradientPattern(value) {
			return p.paintColor(value, box, mark.Style.RenderingIntent)
		}
		return drawing.paintColor(value, origin, mark.Style.RenderingIntent)
	}
	fillColor, err := p.color(mark.Style.Fill, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	strokePaint, err := p.color(mark.Style.Stroke, mark.Style.RenderingIntent)
	if err != nil {
		return err
	}
	if mark.Fill {
		fillColor, err = paint(mark.Style.Fill)
		if err != nil {
			return err
		}
	}
	if mark.Stroke {
		strokePaint, err = paint(mark.Style.Stroke)
		if err != nil {
			return err
		}
	}
	strokeColor := StrokeColor(*strokePaint)
	clips, err := p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	data, err := drawing.pathDataContext(p.ctx, mark.Path, origin)
	if err != nil {
		return err
	}
	object := PathObject{Boundary: pdfBoundary(box), CTM: ctm, AbbreviatedData: data, Fill: &mark.Fill, Stroke: &mark.Stroke, FillColor: fillColor, StrokeColor: &strokeColor, LineWidth: mark.Style.LineWidth * scale, Cap: []string{"Butt", "Round", "Square"}[mark.Style.Cap], Join: []string{"Miter", "Round", "Bevel"}[mark.Style.Join], MiterLimit: mark.Style.MiterLimit, Clips: clips}
	object.LineWidthSet = object.LineWidth == 0
	if mark.Path.EvenOdd {
		object.Rule = "Even-Odd"
	}
	if len(mark.Style.Dash) > 0 {
		object.DashPattern = pdfDashPattern(mark.Style.Dash, scale)
		phase := mark.Style.DashPhase * scale
		object.DashOffset = &phase
	}
	p.appendSequenceObject(GraphicObject{Type: "PathObject", PathObject: object}, mark.Sequence)
	p.report.PathObjects++
	return nil
}
