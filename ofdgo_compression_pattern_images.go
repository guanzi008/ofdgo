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
)

// compressionPatternImageLimit 限制底纹尺寸分析的变换组合数，超限时保留原图尺寸
const compressionPatternImageLimit = 65536

// compressionPatternKey 复用相同底纹的尺寸分析，平移不影响像素需求
type compressionPatternKey struct {
	pattern *Pattern
	linear  [4]float64
}

// objectPatterns 统计文字和路径的填充及描边底纹，保留绘制参数继承关系
// 入参: object 对象样式及坐标, state 继承状态
// 返回: error 底纹分析错误
func (v *compressionImages) objectPatterns(object PathObject, state RenderState) error {
	fill, stroke := object.FillColor, (*FillColor)(object.StrokeColor)
	if defaults := v.renderer.drawParamDefaults(object.DrawParam, state.Defaults); defaults != nil {
		if fill == nil {
			fill = defaults.FillColor
		}
		if stroke == nil {
			stroke = (*FillColor)(defaults.StrokeColor)
		}
	}
	if (fill == nil || fill.Pattern == nil) && (stroke == nil || stroke.Pattern == nil) {
		return nil
	}
	matrix, _ := renderObjectMatrix(object.Boundary, NewMatrix(object.CTM), state)
	for _, color := range [2]*FillColor{fill, stroke} {
		if color != nil && color.Pattern != nil {
			if err := v.patternImages(color, matrix); err != nil {
				return err
			}
		}
	}
	return nil
}

// patternImages 按独立单元坐标汇总底纹图片及缩略图，不展开重复铺排的单元
// 入参: color 底纹颜色, object 对象到页面矩阵
// 返回: error 取消、循环引用或变换错误
func (v *compressionImages) patternImages(color *FillColor, object Matrix) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	pattern := color.Pattern
	if v.patterns[pattern] {
		return fmt.Errorf("cyclic pattern image reference")
	}
	if !finite(pattern.Width) || !finite(pattern.Height) || pattern.Width <= 0 || pattern.Height <= 0 {
		return fmt.Errorf("invalid pattern cell size")
	}
	matrix := NewMatrix(pattern.CTM)
	switch pattern.RelativeTo {
	case "", "Object":
		matrix = object.Multiply(matrix)
	case "Page":
		if v.patternPage != nil {
			matrix = v.patternPage.Multiply(matrix)
		}
	default:
		return fmt.Errorf("invalid pattern coordinate system")
	}
	if !finite(matrix.a) || !finite(matrix.b) || !finite(matrix.c) || !finite(matrix.d) {
		return fmt.Errorf("invalid pattern transform")
	}
	matrix.e, matrix.f = 0, 0
	key := compressionPatternKey{pattern: pattern, linear: [4]float64{matrix.a, matrix.b, matrix.c, matrix.d}}
	if v.patternVisits[key] {
		return nil
	}
	if len(v.patternVisits) >= compressionPatternImageLimit {
		return fmt.Errorf("pattern image analysis limit exceeded")
	}
	if v.patterns == nil {
		v.patterns = make(map[*Pattern]bool)
		v.patternVisits = make(map[compressionPatternKey]bool)
	}
	v.patternVisits[key] = true
	v.patterns[pattern] = true
	defer delete(v.patterns, pattern)
	if pattern.CellContent.Thumbnail != "" {
		id := editorResourceID(pattern.CellContent.Thumbnail)
		if id == "" {
			return fmt.Errorf("invalid pattern thumbnail resource")
		}
		size := compressionImageSize(pattern.Width*math.Hypot(matrix.a, matrix.b), pattern.Height*math.Hypot(matrix.c, matrix.d), v.dpi)
		if err := v.recordSize([2]string{id}, size); err != nil {
			return err
		}
	}
	columns, rows := 1, 1
	switch pattern.ReflectMethod {
	case "", "Normal":
	case "Column":
		columns = 2
	case "Row":
		rows = 2
	case "RowAndColumn":
		columns, rows = 2, 2
	default:
		return fmt.Errorf("invalid pattern reflection")
	}
	parent := v.patternPage
	defer func() { v.patternPage = parent }()
	solid := FillColor{Value: color.Value, Index: color.Index, ColorSpace: color.ColorSpace}
	defaults := &DrawParam{FillColor: &solid, StrokeColor: (*StrokeColor)(&solid)}
	for column := range columns {
		for row := range rows {
			cell := matrix.Multiply(Matrix{a: float64(1 - 2*column), d: float64(1 - 2*row)})
			v.patternPage = &cell
			for member := range pattern.CellContent.objects() {
				if err := v.ctx.Err(); err != nil {
					return err
				}
				if err := v.renderer.WalkObject(&member, RenderState{Defaults: defaults, Parent: &cell, BoundaryInCTM: true}, v); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
