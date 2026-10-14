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
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/xiaoqidun/pdfgo"
)

// text 保留字形及逐字基线，外部字体优先复用同名内嵌资源
// 入参: mark PDF文字绘制信息
// 返回: error 错误信息
func (p *pdfImporter) text(mark pdfgo.TextMark) error {
	if mark.Font.Subtype == pdfgo.Name("Type3") && mark.Mode == 3 {
		return nil
	}
	if mark.Style.BlendMode != "" && mark.Style.BlendMode != "Normal" && mark.Style.BlendMode != "Compatible" {
		return &pdfgo.UnsupportedError{Feature: "text blend mode " + string(mark.Style.BlendMode)}
	}
	if mark.Font.Subtype == pdfgo.Name("Type3") {
		if err := p.flushPath(); err != nil {
			return err
		}
		visitor := p.visitor()
		for index := range mark.Glyphs {
			if err := p.reader.WalkType3Glyph(p.ctx, mark, index, visitor); err != nil {
				return err
			}
		}
		return p.flushPath()
	}
	if mark.Size == 0 || mark.HorizontalScale == 0 {
		return p.degenerateText(mark)
	}
	if mark.Size < 0 || mark.HorizontalScale < 0 {
		x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
		mark.Matrix = mark.Matrix.Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		mark.Size, mark.HorizontalScale = math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
		positions := make([]pdfgo.Point, len(mark.Positions))
		for index, position := range mark.Positions {
			positions[index] = pdfgo.Point{X: position.X * x, Y: position.Y * y}
		}
		mark.Positions = positions
	}
	if err := p.flushPath(); err != nil {
		return err
	}
	var err error
	mark.Style, err = p.maskStyle(mark.Style)
	if err != nil {
		if mark.Clip == nil && p.rasterMaskAllowed(err) {
			mask := mark.Style.SoftMask
			mark.Style.SoftMask = nil
			masked := mark
			return p.rasterGroup(pdfgo.GroupMark{Alpha: 1, SoftMask: mask}, func(v pdfgo.Visitor) error { return v.Text(masked) })
		}
		return err
	}
	paintMode := mark.Mode % 4
	if (paintMode == 0 || paintMode == 2) && mark.Style.FillOverprint && pdfOverprintNeedsSeparation(mark.Style.Fill) || (paintMode == 1 || paintMode == 2) && mark.Style.StrokeOverprint && pdfOverprintNeedsSeparation(mark.Style.Stroke) {
		return &pdfgo.UnsupportedError{Feature: "text color separation overprint"}
	}
	if p.warning != nil && ((paintMode == 0 || paintMode == 2) && p.gradientError(mark.Style.Fill) != nil || (paintMode == 1 || paintMode == 2) && p.gradientError(mark.Style.Stroke) != nil) {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gradient := mark
		return p.compositeRegion(nil, pdfCompositeNode{text: &gradient}, &pdfgo.ColorSpace{Model: "DeviceRGB"}, false)
	}
	font := mark.Font
	embedded := len(font.Program) != 0
	if embedded && font.ProgramType != "FontFile" && font.ProgramType != "FontFile2" && font.ProgramType != "OpenType" && font.ProgramType != "Type1C" && font.ProgramType != "CIDFontType0C" {
		return &pdfgo.UnsupportedError{Feature: "font program " + string(font.ProgramType)}
	}
	var imported *pdfImportedFont
	reused := false
	if embedded {
		imported, err = p.importedFont(font)
		if err != nil {
			return err
		}
	} else {
		imported = p.embeddedTextFont(font, mark.Glyphs)
		symbol := false
		if imported == nil {
			imported, err = p.symbolFont(font)
			if err != nil {
				return err
			}
			symbol = imported != nil
		}
		if imported != nil {
			reused, embedded = true, true
			mark.Glyphs = slices.Clone(mark.Glyphs)
			for index := range mark.Glyphs {
				glyph := &mark.Glyphs[index]
				if symbol {
					glyph.ID = imported.type1Glyphs[glyph.Name]
				} else {
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					glyph.ID = imported.metrics.GlyphIndex(char)
				}
				glyph.HasID = true
			}
		}
	}
	id := p.fontIDs[font]
	if reused {
		id, err = p.editor.addFontResource(imported.resource, imported.checksum)
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.editor.fontMetrics[id] = imported.metrics
	} else if id == "" {
		var err error
		if embedded {
			if imported.repairLimit != 0 {
				p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF embedded font lacks trailing side bearings; unused metrics reconstructed from glyph bounds"})
			}
			id, err = p.editor.addFontResource(imported.resource, imported.checksum)
			if err == nil {
				p.editor.fontMetrics[id] = imported.metrics
			}
		} else {
			id, err = p.editor.AddExternalFont(font.Name)
		}
		if err != nil {
			return fmt.Errorf("PDF font %s resource: %w", font.Name, err)
		}
		p.fontIDs[font] = id
	}
	if embedded && imported.repairLimit != 0 {
		for _, glyph := range mark.Glyphs {
			if glyph.HasID && glyph.ID >= imported.repairLimit {
				return &pdfgo.UnsupportedError{Feature: "embedded font glyph with missing side bearing"}
			}
		}
	}
	var metrics FontMetrics
	var outline FontOutlines
	if embedded {
		metrics = imported.metrics
		outline = metrics.(FontOutlines)
	}
	fill, stroke, visible := paintMode == 0 || paintMode == 2, paintMode == 1 || paintMode == 2, paintMode != 3
	needOutline := stroke
	for _, glyph := range mark.Glyphs {
		needOutline = needOutline || glyph.Name == ".notdef"
	}
	if needOutline && !embedded {
		if resolved, err := p.editor.editorFont(id); err == nil {
			if provider, ok := resolved.(FontOutlines); ok {
				metrics, outline = resolved, provider
				for _, glyph := range mark.Glyphs {
					if glyph.Name == ".notdef" {
						continue
					}
					char, _ := utf8.DecodeRuneInString(glyph.Text)
					if utf8.RuneCountInString(glyph.Text) != 1 || resolved.GlyphIndex(char) == 0 && char != 0 {
						metrics, outline = nil, nil
						break
					}
				}
			}
		}
	}
	outlineStroke := stroke && outline != nil
	var glyphPaths []pdfgo.Path
	const unit = 25.4 / 72
	m := p.matrix.Mul(mark.Matrix).Mul(pdfgo.Matrix{1 / unit, 0, 0, -1 / unit, 0, 0})
	var extent pdfPointBounds
	object := TextObject{Font: id, Size: mark.Size * unit, HScale: mark.HorizontalScale, TextCode: make([]TextCode, 0, len(mark.Glyphs))}
	if embedded {
		object.CGTransform = make([]CGTransform, 0, len(mark.Glyphs))
	}
	position := 0
	for n, glyph := range mark.Glyphs {
		if err := p.ctx.Err(); err != nil {
			return err
		}
		gid := glyph.ID
		undefined := glyph.Name == ".notdef"
		if !embedded && undefined {
			gid = 0
		}
		if embedded && !reused && font.ProgramType == "FontFile" {
			gid = imported.type1Glyphs[glyph.Name]
			glyph.HasID = true
		}
		if glyph.Text == "" && (glyph.HasID || undefined) {
			glyph.Text = "\u00a4"
		}
		if embedded && !glyph.HasID {
			if utf8.RuneCountInString(glyph.Text) != 1 {
				return &pdfgo.UnsupportedError{Feature: "simple font glyph ligature mapping"}
			}
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
			if gid == 0 && char != 0 {
				return fmt.Errorf("missing PDF glyph for %U", char)
			}
		}
		if embedded && gid >= metrics.NumGlyphs() {
			return fmt.Errorf("PDF glyph index %d outside embedded font %s (%d glyphs)", gid, font.Name, metrics.NumGlyphs())
		}
		if !embedded && glyph.Text == "" {
			return &pdfgo.UnsupportedError{Feature: "unmapped external font character"}
		}
		if outlineStroke && !embedded && !undefined {
			char, _ := utf8.DecodeRuneInString(glyph.Text)
			gid = metrics.GlyphIndex(char)
		}
		origin := mark.Positions[n]
		object.TextCode = append(object.TextCode, TextCode{X: pdfNumbers(origin.X * unit), Y: pdfNumbers(-origin.Y * unit), Value: escapeOFDText(glyph.Text)})
		count := utf8.RuneCountInString(glyph.Text)
		if embedded || undefined {
			object.CGTransform = append(object.CGTransform, CGTransform{CodePosition: position, CodeCount: count, GlyphCount: 1, Glyphs: strconv.Itoa(int(gid))})
		}
		position += count
		if embedded || outlineStroke || undefined && outline != nil {
			var path GeometryPath
			var bounds Box
			var err error
			if provider, ok := outline.(FontGlyphBounds); ok && !outlineStroke {
				bounds, err = provider.GlyphBounds(gid, object.Size)
			} else {
				path, err = outline.GlyphOutline(gid, object.Size)
				if err == nil {
					bounds, err = path.Bounds()
				}
			}
			if err != nil {
				return fmt.Errorf("PDF font %s outline: %w", font.Name, err)
			}
			if diagnostics, ok := outline.(FontGlyphDiagnostics); ok {
				if err := diagnostics.GlyphWarning(gid); err != nil {
					if p.warning == nil {
						return fmt.Errorf("PDF font %s glyph %d hinting: %w", font.Name, gid, err)
					}
					if p.fontWarnings == nil {
						p.fontWarnings = make(map[string]bool)
					}
					if !p.fontWarnings[id] {
						p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid hinting instructions; design outlines and original font retained", font.Name)})
						p.fontWarnings[id] = true
					}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X*mark.HorizontalScale, Y: -origin.Y*unit + point.Y}))
			}
			if outlineStroke {
				matrix := mark.Matrix.Mul(pdfgo.Matrix{mark.HorizontalScale / unit, 0, 0, -1 / unit, origin.X, origin.Y})
				glyphPath, err := pdfGlyphPath(path, matrix)
				if err != nil {
					return err
				}
				glyphPaths = append(glyphPaths, glyphPath)
			}
		} else {
			width := glyph.Width / 1000 * object.Size * mark.HorizontalScale
			bounds := Box{X: 0, Y: -object.Size, W: width, H: object.Size * 1.25}
			if undefined {
				b := font.BoundingBox
				if b == nil {
					b = font.FallbackBoundingBox
				}
				if b != nil {
					bounds = Box{X: b.XMin / 1000 * object.Size * mark.HorizontalScale, Y: -b.YMax / 1000 * object.Size, W: (b.XMax - b.XMin) / 1000 * object.Size * mark.HorizontalScale, H: (b.YMax - b.YMin) / 1000 * object.Size}
				}
			}
			for _, point := range []pdfgo.Point{{X: bounds.X, Y: bounds.Y}, {X: bounds.X + bounds.W, Y: bounds.Y}, {X: bounds.X, Y: bounds.Y + bounds.H}, {X: bounds.X + bounds.W, Y: bounds.Y + bounds.H}} {
				extent.add(m.Apply(pdfgo.Point{X: origin.X*unit + point.X, Y: -origin.Y*unit + point.Y}))
			}
		}
	}
	if mark.Replacement != nil {
		pdfApplyReplacement(&object, mark.Replacement.Text, mark.Positions)
	}
	box := extent.box()
	if stroke && !outlineStroke {
		strokeMatrix := mark.StrokeMatrix
		if strokeMatrix == (pdfgo.Matrix{}) {
			strokeMatrix = pdfgo.Identity()
		}
		sx, sy := math.Hypot(strokeMatrix[0], strokeMatrix[1]), math.Hypot(strokeMatrix[2], strokeMatrix[3])
		if math.Abs(sx-sy) > 1e-8*math.Max(1, sx) || math.Abs(strokeMatrix[0]*strokeMatrix[2]+strokeMatrix[1]*strokeMatrix[3]) > 1e-8*math.Max(1, sx*sy) {
			return &pdfgo.UnsupportedError{Feature: "anisotropic text stroke"}
		}
		scale := math.Sqrt(math.Abs(m[0]*m[3] - m[1]*m[2]))
		if scale == 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			return &pdfgo.UnsupportedError{Feature: "degenerate text stroke matrix"}
		}
		lineWidth := mark.Style.LineWidth * math.Hypot(p.matrix[0], p.matrix[1]) * sx
		margin := lineWidth / 2 * math.Max(1, mark.Style.MiterLimit)
		box = Box{X: box.X - margin, Y: box.Y - margin, W: box.W + 2*margin, H: box.H + 2*margin}
		object.LineWidth = lineWidth / scale
		object.LineWidthSet = object.LineWidth == 0
		object.Cap = []string{"Butt", "Round", "Square"}[mark.Style.Cap]
		object.Join = []string{"Miter", "Round", "Bevel"}[mark.Style.Join]
		object.MiterLimit = mark.Style.MiterLimit
		if len(mark.Style.Dash) > 0 {
			strokeScale := math.Hypot(p.matrix[0], p.matrix[1]) * sx / scale
			object.DashPattern = pdfDashPattern(mark.Style.Dash, strokeScale)
			phase := mark.Style.DashPhase * strokeScale
			object.DashOffset = &phase
		}
	}
	object.Boundary = pdfBoundary(box)
	if stroke && !outlineStroke {
		strokePaint, err := p.paintColor(mark.Style.Stroke, box)
		if err != nil {
			return err
		}
		color := StrokeColor(*strokePaint)
		object.StrokeColor = &color
	}
	if box.X >= p.pageWidth || box.Y >= p.pageHeight || box.X+box.W <= 0 || box.Y+box.H <= 0 {
		visible = false
	}
	object.CTM = pdfNumbers(m[0], m[1], m[2], m[3], m[4]-box.X, m[5]-box.Y)
	object.Fill = &fill
	object.Stroke = &stroke
	object.Visible = &visible
	object.FillColor = p.color(mark.Style.Fill)
	if fill && !outlineStroke {
		object.FillColor, err = p.paintColor(mark.Style.Fill, box)
		if err != nil {
			return err
		}
	}
	object.Clips, err = p.clips(mark.Style.Clips, box)
	if err != nil {
		return err
	}
	if mark.Clip != nil {
		clipObject := object
		clipObject.Clips = nil
		clipObject.Fill, clipObject.Stroke = new(bool), new(bool)
		*clipObject.Fill = true
		*clipObject.Stroke = false
		clipObject.Visible = nil
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = []TextObject{clipObject}
	}
	if outlineStroke {
		for _, path := range glyphPaths {
			if len(path.Segments) == 0 {
				continue
			}
			if err := p.ctx.Err(); err != nil {
				return err
			}
			matrix := mark.StrokeMatrix
			if matrix == (pdfgo.Matrix{}) {
				matrix = pdfgo.Identity()
			}
			if err := p.appendPath(pdfgo.PathMark{Path: path, Style: mark.Style, Fill: fill, Stroke: true, StrokeMatrix: matrix}); err != nil {
				return err
			}
		}
		object.Visible = new(bool)
		object.Fill, object.Stroke = new(bool), new(bool)
	}
	p.appendObject(GraphicObject{Type: "TextObject", TextObject: object})
	p.report.TextObjects++
	return nil
}

// degenerateText 保留零字号或零水平缩放的逐字位置与退化字形
// 入参: mark PDF文字绘制信息
// 返回: error 转换错误
func (p *pdfImporter) degenerateText(mark pdfgo.TextMark) error {
	x, y := math.Copysign(1, mark.Size*mark.HorizontalScale), math.Copysign(1, mark.Size)
	size, scale := math.Abs(mark.Size), math.Abs(mark.HorizontalScale)
	if size == 0 {
		size, x, y = 1, 0, 0
	}
	if scale == 0 {
		scale, x = 1, 0
	}
	var clips []TextObject
	for index, position := range mark.Positions {
		glyph := mark
		glyph.Size, glyph.HorizontalScale = size, scale
		glyph.Matrix = mark.Matrix.Mul(pdfgo.Matrix{1, 0, 0, 1, position.X, position.Y}).Mul(pdfgo.Matrix{x, 0, 0, y, 0, 0})
		glyph.Positions = []pdfgo.Point{{}}
		glyph.Glyphs = mark.Glyphs[index : index+1]
		if mark.Clip != nil {
			glyph.Clip = new(pdfgo.TextClip)
		}
		if err := p.text(glyph); err != nil {
			return err
		}
		if glyph.Clip != nil {
			clips = append(clips, p.clipTexts[glyph.Clip]...)
			delete(p.clipTexts, glyph.Clip)
		}
	}
	if mark.Clip != nil {
		if p.clipTexts == nil {
			p.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		}
		p.clipTexts[mark.Clip] = clips
	}
	return nil
}

// disjointText 检查实际字形轮廓是否互不相交，允许无重叠挖空文字直接保留
// 入参: node 隐式文字组
// 返回: bool 是否互不相交, error 轮廓编译错误
func (p *pdfImporter) disjointText(node pdfCompositeNode) (bool, error) {
	cache := p.compositingCache()
	if result, found := cache.textDisjoint[node.group]; found {
		return result, nil
	}
	geometry, err := p.renderer.Geometry()
	if err != nil {
		return false, err
	}
	var inverse pdfgo.Matrix
	var boxes []Box
	for _, child := range node.children {
		mark := child.text
		if mark == nil {
			mark = child.glyph
		}
		if mark == nil || len(mark.Glyphs) != 1 {
			return false, nil
		}
		if inverse == (pdfgo.Matrix{}) {
			var ok bool
			inverse, ok = p.matrix.Mul(mark.Matrix).Inverse()
			if !ok {
				return false, nil
			}
		}
		scene, _, err := p.compileGroup(func(local *pdfImporter) error {
			if child.group != nil {
				return child.coverage(local, false)
			}
			_, fill, stroke := child.style()
			if fill {
				if err := child.coverage(local, false); err != nil {
					return err
				}
			}
			if stroke {
				return child.coverage(local, true)
			}
			return nil
		})
		if err != nil {
			return false, err
		}
		if scene == nil {
			continue
		}
		var box Box
		for _, command := range scene.Commands {
			path, err := rasterCommandGeometry(geometry, command)
			if err != nil {
				return false, err
			}
			path, err = geometry.Transform(path, MatrixFromValues([6]float64(inverse)))
			if err != nil {
				return false, err
			}
			bounds, err := geometry.Bounds(path)
			if err != nil {
				return false, err
			}
			box = unionTextBox(box, bounds)
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		for _, previous := range boxes {
			if box.X < previous.X+previous.W && previous.X < box.X+box.W && box.Y < previous.Y+previous.H && previous.Y < box.Y+box.H {
				if cache.textDisjoint == nil {
					cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
				}
				cache.textDisjoint[node.group] = false
				return false, nil
			}
		}
		boxes = append(boxes, box)
	}
	if cache.textDisjoint == nil {
		cache.textDisjoint = make(map[*pdfgo.GroupMark]bool)
	}
	cache.textDisjoint[node.group] = true
	return true, nil
}
