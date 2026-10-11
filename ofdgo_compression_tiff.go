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
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"math"
)

// optimizeTIFFResource 压缩单目录TIFF，保留方向、透明度、采样精度及像素密度
// 入参: ctx 取消上下文, data TIFF编码, options 压缩选项, size 显示像素需求
// 返回: []byte 图片编码，不可完整转换时保留原文, error 编码或取消错误
func optimizeTIFFResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	layout, err := readTIFFLayout(bytes.NewReader(data))
	if err != nil {
		return data, ctx.Err()
	}
	pixelSize := int64(8)
	if layout.planar {
		pixelSize = 16
	}
	if int64(layout.width) > outputOptimizationBufferLimit/pixelSize/int64(layout.height) {
		return data, ctx.Err()
	}
	density, ok := tiffCompressionDensity(data, layout)
	if !ok {
		return data, ctx.Err()
	}
	pixels, err := decodeTIFFImage(ctx, data, layout)
	if err != nil {
		return data, ctx.Err()
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, density)
}

// tiffCompressionDensity 校验可转换的TIFF字段并提取规范化后的像素密度
// 色彩配置、预乘透明度、扩展字段及其他目录需保留原始编码
// 入参: data TIFF编码, layout 已校验的主图布局
// 返回: rasterDensity PNG像素密度, bool 是否可完整转换
func tiffCompressionDensity(data []byte, layout tiffImageLayout) (rasterDensity, bool) {
	var density rasterDensity
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	if layout.next != 0 || layout.offset != order.Uint32(data[4:]) || layout.reduced {
		return density, false
	}
	offset := int(layout.offset)
	count := int(order.Uint16(data[offset:]))
	unit := uint32(2)
	var resolutions [2][2]uint32
	for i := range count {
		entry := data[offset+2+i*12 : offset+14+i*12]
		tag, kind, number := order.Uint16(entry), order.Uint16(entry[2:]), order.Uint32(entry[4:])
		value := order.Uint32(entry[8:])
		if kind == 3 && number == 1 {
			value = uint32(order.Uint16(entry[8:]))
		}
		switch tag {
		case 256, 257, 258, 259, 262, 273, 274, 277, 278, 279, 320, 322, 323, 324, 325:
		case 254:
			if value != 0 {
				return density, false
			}
		case 255:
			if value != 1 {
				return density, false
			}
		case 266:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 284:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 292, 293:
			mask := uint32(7)
			if tag == 293 {
				mask = 2
			}
			if kind != 4 || number != 1 || value & ^mask != 0 {
				return density, false
			}
		case 317:
			if kind != 3 || number != 1 || value != 1 && value != 2 {
				return density, false
			}
		case 338:
			if kind != 3 || number != 1 || value != 2 {
				return density, false
			}
		case 339:
			if kind != 3 || number == 0 || number > 4 {
				return density, false
			}
			values := entry[8:]
			if number > 2 {
				if uint64(value)+uint64(number)*2 > uint64(len(data)) {
					return density, false
				}
				values = data[value:]
			}
			for j := uint32(0); j < number; j++ {
				if order.Uint16(values[j*2:]) != 1 {
					return density, false
				}
			}
		case 282, 283:
			if kind != 5 || number != 1 || uint64(value)+8 > uint64(len(data)) {
				return density, false
			}
			resolutions[tag-282] = [2]uint32{order.Uint32(data[value:]), order.Uint32(data[value+4:])}
		case 296:
			if kind != 3 || number != 1 || value < 1 || value > 3 {
				return density, false
			}
			unit = value
		default:
			return density, false
		}
	}
	if resolutions == [2][2]uint32{} {
		return density, true
	}
	x, y := resolutions[0], resolutions[1]
	if x[0] == 0 || x[1] == 0 || y[0] == 0 || y[1] == 0 {
		return density, false
	}
	if unit == 1 {
		a, b := uint64(x[0])*uint64(y[1]), uint64(y[0])*uint64(x[1])
		divisor, next := a, b
		for next != 0 {
			divisor, next = next, divisor%next
		}
		density.x, density.y = float64(a/divisor), float64(b/divisor)
	} else {
		factor := 1 / 0.0254
		if unit == 3 {
			factor = 100
		}
		density = rasterDensity{x: float64(x[0]) / float64(x[1]) * factor, y: float64(y[0]) / float64(y[1]) * factor, unit: 1}
	}
	if math.Round(density.x) < 1 || math.Round(density.y) < 1 || math.Round(density.x) > math.MaxInt32 || math.Round(density.y) > math.MaxInt32 {
		return density, false
	}
	if layout.orientation >= 5 {
		density.x, density.y = density.y, density.x
	}
	return density, true
}
