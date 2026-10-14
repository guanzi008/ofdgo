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
	"image"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"golang.org/x/image/draw"
)

// PixelTransform 将页面毫米变换转换为像素变换，保留尺寸取整后的原点位置
// 入参: m 局部到页面变换, height 目标像素高度
// 返回: RasterMatrix 局部到像素变换
func (p *RasterPage) PixelTransform(m RasterMatrix, height int) RasterMatrix {
	scale := p.DPI / 25.4
	for i := range m {
		m[i] *= scale
	}
	m[5] += float64(height) - p.Height*scale
	return m
}

// drawRasterImageCommand 使用当前后端生成裁剪蒙版，共用高质量图像采样
// 入参: backend 当前后端, page 页面, dst 目标图像, src 原始图像, command 图像指令, cache 蒙版缓存
// 返回: error 裁剪绘制错误
func drawRasterImageCommand(backend RasterBackend, page *RasterPage, dst draw.Image, src image.Image, command RasterCommand, cache *renderCache[[32]byte, *image.Alpha]) error {
	m := page.PixelTransform(command.Transform, dst.Bounds().Dy())
	if command.Clip == nil {
		baseimage.DrawPixels(dst, src, m, draw.Over)
		return nil
	}
	mask, err := rasterClipMask(backend, page, command.Clip, cache)
	if err != nil {
		return err
	}
	if mask.Rect.Empty() {
		return nil
	}
	bounds := mask.Rect.Intersect(dst.Bounds()).Intersect(baseimage.TransformBounds(src.Bounds(), m, dst.Bounds()))
	if bounds.Empty() {
		return nil
	}
	layer := image.NewRGBA64(bounds)
	baseimage.DrawPixels(layer, baseimage.PixelSource(src), m, draw.Src)
	draw.DrawMask(dst, bounds, layer, bounds.Min, mask, bounds.Min, draw.Over)
	return nil
}
