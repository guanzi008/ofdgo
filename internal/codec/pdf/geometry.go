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

package pdf

import (
	"context"
	"fmt"
	"math"
	"strconv"

	basegeom "github.com/xiaoqidun/ofdgo/internal/base/geom"
	"github.com/xiaoqidun/pdfgo"
)

// GeometryPath 直接转换PDF路径坐标，不经文本编码或曲线展平
// 入参: ctx 取消上下文, source PDF路径, matrix 坐标变换
// 返回: basegeom.Path 独立几何路径, error 指令、坐标或取消错误
func GeometryPath(ctx context.Context, source pdfgo.Path, matrix pdfgo.Matrix) (basegeom.Path, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := make(basegeom.Path, len(source.Segments))
	for i, segment := range source.Segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var count int
		switch segment.Operator {
		case "M":
			path[i].Verb, count = basegeom.Move, 1
		case "L":
			path[i].Verb, count = basegeom.Line, 1
		case "B":
			path[i].Verb, count = basegeom.Cubic, 3
		case "C":
			path[i].Verb = basegeom.ClosePath
		default:
			return nil, fmt.Errorf("invalid PDF path operator %q", segment.Operator)
		}
		if len(segment.Points) != count {
			return nil, fmt.Errorf("invalid PDF path operands for %s", segment.Operator)
		}
		var points [3]basegeom.Point
		for j, point := range segment.Points {
			point = matrix.Apply(point)
			points[j] = basegeom.Point{X: point.X, Y: point.Y}
		}
		if count != 0 {
			path[i].End = points[count-1]
		}
		if count == 3 {
			path[i].Control1, path[i].Control2 = points[0], points[1]
		}
	}
	if err := path.Validate(); err != nil {
		return nil, err
	}
	return path, ctx.Err()
}

// GlyphPath 将字形轮廓变换到PDF页面坐标，二次曲线精确转换为三次曲线
// 入参: outline 字形轮廓, matrix 字形到页面的变换
// 返回: pdfgo.Path 页面路径, error 非字体轮廓指令
func GlyphPath(outline basegeom.Path, matrix pdfgo.Matrix) (pdfgo.Path, error) {
	path := pdfgo.Path{Segments: make([]pdfgo.Segment, 0, len(outline))}
	var current, start basegeom.Point
	point := func(p basegeom.Point) pdfgo.Point { return matrix.Apply(pdfgo.Point{X: p.X, Y: p.Y}) }
	for _, segment := range outline {
		switch segment.Verb {
		case basegeom.Move:
			start = segment.End
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "M", Points: []pdfgo.Point{point(segment.End)}})
		case basegeom.Line:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "L", Points: []pdfgo.Point{point(segment.End)}})
		case basegeom.Quad:
			c1 := basegeom.Point{X: current.X + (segment.Control1.X-current.X)*2/3, Y: current.Y + (segment.Control1.Y-current.Y)*2/3}
			c2 := basegeom.Point{X: segment.End.X + (segment.Control1.X-segment.End.X)*2/3, Y: segment.End.Y + (segment.Control1.Y-segment.End.Y)*2/3}
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "B", Points: []pdfgo.Point{point(c1), point(c2), point(segment.End)}})
		case basegeom.Cubic:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "B", Points: []pdfgo.Point{point(segment.Control1), point(segment.Control2), point(segment.End)}})
		case basegeom.ClosePath:
			path.Segments = append(path.Segments, pdfgo.Segment{Operator: "C"})
			current = start
			continue
		default:
			return pdfgo.Path{}, fmt.Errorf("unsupported PDF glyph geometry verb %d", segment.Verb)
		}
		current = segment.End
	}
	return path, nil
}

// PathBounds 逐段计算变换后的控制点边界，不创建临时点集合
// 入参: ctx 取消上下文, path PDF路径, matrix 坐标变换
// 返回: basegeom.Box 控制点边界，空路径返回零值, error 取消错误
func PathBounds(ctx context.Context, path pdfgo.Path, matrix pdfgo.Matrix) (basegeom.Box, error) {
	if err := ctx.Err(); err != nil {
		return basegeom.Box{}, err
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	found := false
	for _, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return basegeom.Box{}, err
		}
		for index, point := range segment.Points {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return basegeom.Box{}, err
				}
			}
			point = matrix.Apply(point)
			minX, minY = math.Min(minX, point.X), math.Min(minY, point.Y)
			maxX, maxY = math.Max(maxX, point.X), math.Max(maxY, point.Y)
			found = true
		}
	}
	if err := ctx.Err(); err != nil {
		return basegeom.Box{}, err
	}
	if !found {
		return basegeom.Box{}, nil
	}
	return basegeom.Box{X: minX, Y: minY, W: math.Max(maxX-minX, 1e-6), H: math.Max(maxY-minY, 1e-6)}, nil
}

// PathData 转换路径并在分段处理时检查取消
// 入参: ctx 取消上下文, path PDF路径, matrix 坐标变换, origin 对象边界
// 返回: string OFD路径数据, error 取消错误
func PathData(ctx context.Context, path pdfgo.Path, matrix pdfgo.Matrix, origin basegeom.Box) (string, error) {
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
			point = matrix.Apply(point)
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
