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
	"fmt"
	"image"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
)

// imageWithEffects 合并同尺寸二值蒙版和对象透明度，保留原图采样精度及只读像素
// 入参: ctx 取消上下文，nil使用后台上下文, img 原图, mask 二值蒙版，可为空, alpha 对象透明度，可为空
// 返回: image.Image 处理后的图片, error 蒙版、解码或取消错误
func imageWithEffects(ctx context.Context, img, mask image.Image, alpha *int) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := 255
	if alpha != nil {
		a = clampColor(*alpha)
	}
	if img == nil || mask == nil && a == 255 {
		return img, nil
	}
	source, err := baseimage.PixelDataContext(ctx, img)
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	var maskBounds image.Rectangle
	if mask != nil {
		mask, err = baseimage.PixelDataContext(ctx, mask)
		if err != nil {
			return nil, err
		}
		maskBounds = mask.Bounds()
		if bounds.Empty() || bounds.Size() != maskBounds.Size() {
			return nil, fmt.Errorf("image mask dimensions differ from source")
		}
	}
	if baseimage.HighPrecision(source) {
		out := image.NewNRGBA64(bounds)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				opacity := a
				if mask != nil {
					value, err := imageMaskAlpha(mask, maskBounds.Min.X+x-bounds.Min.X, maskBounds.Min.Y+y-bounds.Min.Y)
					if err != nil {
						return nil, err
					}
					opacity *= int(value) / 255
				}
				pixel := baseimage.NRGBA64At(source, x, y)
				pixel.A = uint16(uint32(pixel.A) * uint32(opacity) / 255)
				out.SetNRGBA64(x, y, pixel)
			}
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return out, nil
	}
	out := image.NewNRGBA(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		row := out.Pix[(y-bounds.Min.Y)*out.Stride : (y-bounds.Min.Y+1)*out.Stride]
		if src, ok := source.(*image.NRGBA); ok {
			offset := src.PixOffset(bounds.Min.X, y)
			copy(row, src.Pix[offset:offset+len(row)])
		} else {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				out.SetNRGBA(x, y, baseimage.NRGBAAt(source, x, y))
			}
		}
		for x := 0; x < bounds.Dx(); x++ {
			if x&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			opacity := a
			if mask != nil {
				value, err := imageMaskAlpha(mask, maskBounds.Min.X+x, maskBounds.Min.Y+y-bounds.Min.Y)
				if err != nil {
					return nil, err
				}
				opacity *= int(value) / 255
			}
			row[x*4+3] = uint8(int(row[x*4+3]) * opacity / 255)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// imageMaskAlpha 读取二值蒙版，拒绝灰阶、彩色及带透明度的像素
// 入参: mask 蒙版图像, x 横坐标, y 纵坐标
// 返回: uint8 透明度，黑色为0、白色为255, error 非二值像素错误
func imageMaskAlpha(mask image.Image, x, y int) (uint8, error) {
	var r, g, b, a uint32
	if source, ok := mask.(image.RGBA64Image); ok {
		pixel := source.RGBA64At(x, y)
		r, g, b, a = uint32(pixel.R), uint32(pixel.G), uint32(pixel.B), uint32(pixel.A)
	} else {
		r, g, b, a = mask.At(x, y).RGBA()
	}
	if a != 65535 || r != g || r != b || r != 0 && r != 65535 {
		return 0, fmt.Errorf("image mask contains non-binary pixel at (%d, %d)", x, y)
	}
	return uint8(r >> 8), nil
}

// validateImageMask 校验二值蒙版尺寸及像素，不分配合成图像
// 入参: ctx 取消上下文, mask 蒙版图像, size 原图尺寸
// 返回: error 解码、尺寸、像素或取消错误
func validateImageMask(ctx context.Context, mask image.Image, size image.Point) error {
	if mask.Bounds().Size() != size || size.X <= 0 || size.Y <= 0 {
		return fmt.Errorf("image mask dimensions differ from source")
	}
	mask, err := baseimage.PixelDataContext(ctx, mask)
	if err != nil {
		return err
	}
	bounds := mask.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&255 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			if _, err := imageMaskAlpha(mask, x, y); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
