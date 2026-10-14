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
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// OutputColor 将合成结果映射到OFD输出，与保留的设备四色对象使用相同显示换算
// 入参: space 合成空间, values 非预乘分量
// 返回: [3]float64 显示颜色, error 校准颜色转换错误
func OutputColor(space *pdfgo.ColorSpace, values [4]float64) ([3]float64, error) {
	if space.Model == "DeviceCMYK" && !space.Calibrated() {
		black := 1 - values[3]
		return [3]float64{(1 - values[0]) * black, (1 - values[1]) * black, (1 - values[2]) * black}, nil
	}
	return space.RGB(values[:space.Components()], "RelativeColorimetric")
}

// NormalBlend 判断混合模式是否为普通覆盖
// 入参: mode 混合模式
// 返回: bool 是否普通覆盖
func NormalBlend(mode pdfgo.Name) bool {
	return mode == "" || mode == "Normal" || mode == "Compatible"
}

// NativePaint 使用库层色料定义求值设备四色，不以备用色代替原生浓度
// 入参: paint 源画刷, space 设备四色空间, intent 渲染意图
// 返回: *pdfgo.Paint 原生画刷或nil, [4]bool 显式通道, error 色料定义错误
func NativePaint(paint pdfgo.Paint, space *pdfgo.ColorSpace, intent pdfgo.Name) (*pdfgo.Paint, [4]bool, error) {
	if paint.Colorant == nil {
		return nil, [4]bool{}, nil
	}
	result, err := paint.Colorant.Space.ResolveInGroup(paint.Colorant.Tints, &pdfgo.ColorantDevice{Space: space}, space, intent, false)
	if err != nil || len(result.Alternates) != 0 {
		return nil, [4]bool{}, err
	}
	return result.Process, result.ProcessMask, nil
}

// CompositeColor 将原始画刷变换到混合空间，渐变在源空间求值
// 入参: paint 画刷, point 页面坐标, space 混合空间, intent 渲染意图, conversion 设备转换函数
// 返回: [4]float64 混合分量, bool 是否着色, error 颜色错误
func CompositeColor(paint pdfgo.Paint, point pdfgo.Point, space *pdfgo.ColorSpace, intent pdfgo.Name, conversion pdfgo.ColorConversion) ([4]float64, bool, error) {
	if paint.Shading != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local shading pattern compositing"}
	}
	if paint.Tiling != nil {
		return [4]float64{}, false, &pdfgo.UnsupportedError{Feature: "local tiling pattern compositing"}
	}
	var values [4]float64
	var source *pdfgo.ColorSpace
	if paint.Function != nil {
		g := paint.Function
		if g.Bounds != nil {
			inverse, ok := g.PatternMatrix.Inverse()
			if !ok {
				return values, false, fmt.Errorf("singular function shading bounds transform")
			}
			local := inverse.Apply(point)
			if local.X < g.Bounds.XMin || local.X > g.Bounds.XMax || local.Y < g.Bounds.YMin || local.Y > g.Bounds.YMax {
				return values, false, nil
			}
		}
		inverse, ok := g.Matrix.Inverse()
		if !ok {
			return values, false, fmt.Errorf("singular function shading transform")
		}
		local := inverse.Apply(point)
		source, intent = g.Space, g.Intent
		if local.X < g.Domain.XMin || local.X > g.Domain.XMax || local.Y < g.Domain.YMin || local.Y > g.Domain.YMax {
			if g.Background == nil {
				return values, false, nil
			}
			values = *g.Background
		} else {
			var err error
			values, err = g.ValuesAt(local)
			if err != nil {
				return values, false, err
			}
		}
	} else if paint.Axial != nil || paint.Radial != nil {
		var background *[4]float64
		var bounds *pdfgo.Rectangle
		var matrix pdfgo.Matrix
		if g := paint.Axial; g != nil {
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		} else {
			g := paint.Radial
			source, intent = g.Space, g.Intent
			background, bounds, matrix = g.Background, g.Bounds, g.Matrix
		}
		if bounds != nil {
			local := point
			if matrix != (pdfgo.Matrix{}) {
				inverse, ok := matrix.Inverse()
				if !ok {
					return values, false, fmt.Errorf("singular shading transform")
				}
				local = inverse.Apply(point)
			}
			if local.X < bounds.XMin || local.X > bounds.XMax || local.Y < bounds.YMin || local.Y > bounds.YMax {
				return values, false, nil
			}
		}
		t, visible := ShadingPosition(paint, point)
		var err error
		if !visible {
			if background == nil {
				return values, false, nil
			}
			values = *background
		} else if paint.Axial != nil {
			values, err = paint.Axial.ValuesAt(t)
		} else {
			values, err = paint.Radial.ValuesAt(t)
		}
		if err != nil {
			return values, false, err
		}
	} else if paint.Space != nil {
		values, source = paint.Values, paint.Space
	} else if paint.CMYK != nil {
		values, source = *paint.CMYK, &pdfgo.ColorSpace{Model: "DeviceCMYK"}
	} else if paint.SourceSpace == "DeviceGray" {
		values[0], source = paint.RGB[0], &pdfgo.ColorSpace{Model: "DeviceGray"}
	} else {
		copy(values[:], paint.RGB[:])
		source = &pdfgo.ColorSpace{Model: "DeviceRGB"}
	}
	values, err := space.ConvertWith(values[:source.Components()], source, intent, conversion)
	return values, true, err
}
