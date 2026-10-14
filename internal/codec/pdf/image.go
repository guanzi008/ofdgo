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
	"image"
	"image/color"
	"math"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// StencilImage 按当前填充色读取模板图像，不分配整幅彩色样本
type StencilImage struct {
	Source image.Image
	Fill   color.NRGBA64
}

// NativeImage 按源像素输出原生过程色，不分配完整显示颜色缓冲
type NativeImage struct {
	Source *pdfgo.ImageProcess
	Space  *pdfgo.ColorSpace
}

// ColorModel 保留填充色和模板的有效采样精度
// 返回: color.Model 非预乘颜色模型
func (s *StencilImage) ColorModel() color.Model {
	model := s.Source.ColorModel()
	if s.Fill.R%257 == 0 && s.Fill.G%257 == 0 && s.Fill.B%257 == 0 && (model == color.GrayModel || model == color.NRGBAModel || model == color.RGBAModel) {
		return color.NRGBAModel
	}
	return color.NRGBA64Model
}

// Bounds 返回模板图像的采样边界
// 返回: image.Rectangle 图像边界
func (s *StencilImage) Bounds() image.Rectangle { return s.Source.Bounds() }

// At 以模板灰度计算填充覆盖率，不重复变换颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 非预乘颜色
func (s *StencilImage) At(x, y int) color.Color {
	return s.NRGBA64At(x, y)
}

// NRGBA64At 直接读取模板覆盖率，保留填充色的有效精度
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *StencilImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	gray := baseimage.NRGBA64At(s.Source, x, y)
	pixel := s.Fill
	pixel.A = uint16(65535 - gray.R)
	return pixel
}

// ColorModel 保留16位原生分量与源透明度
// 返回: color.Model 非预乘颜色模型
func (s *NativeImage) ColorModel() color.Model { return color.NRGBA64Model }

// Bounds 返回源图像的完整采样边界
// 返回: image.Rectangle 原始像素边界
func (s *NativeImage) Bounds() image.Rectangle { return s.Source.Bounds() }

// At 获取按OFD输出映射的原生颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 非预乘颜色
func (s *NativeImage) At(x, y int) color.Color { return s.NRGBA64At(x, y) }

// NRGBA64At 按原始精度转换设备过程色，不进行页面采样或备用变换
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *NativeImage) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.Bounds()) {
		return color.NRGBA64{}
	}
	values, alpha := s.Source.ValuesAt(x, y)
	rgb, _ := OutputColor(s.Space, values)
	return color.NRGBA64{R: uint16(math.Round(rgb[0] * 65535)), G: uint16(math.Round(rgb[1] * 65535)), B: uint16(math.Round(rgb[2] * 65535)), A: uint16(math.Round(alpha * 65535))}
}
