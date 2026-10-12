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
	"hash/crc32"
	"image"
	"io"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// rasterDensity 保存横纵像素密度，unit为0表示比例、1表示每米像素数
type rasterDensity struct {
	x, y float64
	unit byte
}

// outputImageFormat 识别可由资源压缩生成的图片格式
// 入参: data 图片编码
// 返回: string 标准资源格式，未识别时为空
func outputImageFormat(data []byte) string {
	if isJPEGData(data) {
		return "JPEG"
	}
	if isPNGData(data) {
		return "PNG"
	}
	return ""
}

// optimizeImageResource 将可完整保留的BMP及TIFF转为PNG，再复用图像压缩和降采样策略
// 原始模式及无法保留的色彩配置不转换，仅采用小于原始编码的结果
// 入参: ctx 取消上下文, data 图片编码, options 压缩选项, size 像素需求
// 返回: []byte 图片编码，可能引用输入, error 解码、编码或取消错误
func optimizeImageResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	if err := options.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.Mode != CompressionUnchanged && isTIFFData(data) {
		return optimizeTIFFResource(ctx, data, options, size)
	}
	if options.Mode == CompressionUnchanged || !bytes.HasPrefix(data, []byte("BM")) {
		return pdfgo.OptimizeImageResource(ctx, data, options, size)
	}
	return optimizeBMPResource(ctx, data, options, size)
}

// optimizeBMPResource 压缩可保留色彩与密度的位图，未知元数据及高精度位域保持原样
// 入参: ctx 取消上下文, data BMP编码, options 压缩选项, size 像素需求
// 返回: []byte 图片编码, error 解码、编码或取消错误
func optimizeBMPResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	offset, width, height, depth, compression, err := parseBMPHeader(data)
	if err != nil {
		return data, nil
	}
	if height < 0 {
		height = -height
	}
	if int64(width)*int64(height) > outputOptimizationBufferLimit/4 || compression != 0 && compression != 3 {
		return data, nil
	}
	header := binary.LittleEndian.Uint32(data[14:18])
	if header != 40 && header != 108 && header != 124 {
		return data, nil
	}
	if header >= 108 && binary.LittleEndian.Uint32(data[70:74]) != 0x73524742 {
		return data, nil
	}
	if header == 124 && (binary.LittleEndian.Uint32(data[122:126]) != 0 || binary.LittleEndian.Uint32(data[126:130]) != 0 || binary.LittleEndian.Uint32(data[130:134]) != 0) {
		return data, nil
	}
	stride := (int64(width)*int64(depth) + 31) / 32 * 4
	if int64(offset)+stride*int64(height) != int64(len(data)) {
		return data, nil
	}
	xdpi, ydpi := int32(binary.LittleEndian.Uint32(data[38:42])), int32(binary.LittleEndian.Uint32(data[42:46]))
	if xdpi < 0 || ydpi < 0 {
		return data, nil
	}
	if depth == 16 || depth == 32 {
		masks, err := bmpColorMasks(data, offset, depth, compression)
		if err != nil {
			return data, nil
		}
		if options.Mode == CompressionLossless {
			for _, mask := range masks {
				if mask.max > 255 {
					return data, nil
				}
			}
		}
	}
	var pixels image.Image
	if depth == 16 || compression == 3 {
		pixels, err = decodeBMPImageContext(ctx, data)
	} else {
		pixels, _, err = image.Decode(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
	}
	if err != nil {
		return nil, err
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, rasterDensity{x: float64(xdpi), y: float64(ydpi), unit: 1})
}

// optimizeRasterResource 编码已解码的资源图片，同步降采样后的像素密度，仅采用更小结果
// 绝对密度图片先降采样再编码，密度按最终尺寸换算后量化为PNG整数
// 入参: ctx 取消上下文, data 原编码, pixels 像素, options 压缩选项, size 像素需求, density 像素密度
// 返回: []byte 图片编码, error 编码或取消错误
func optimizeRasterResource(ctx context.Context, data []byte, pixels image.Image, options CompressionOptions, size image.Point, density rasterDensity) ([]byte, error) {
	hasDensity := density.x != 0 || density.y != 0
	if hasDensity && density.unit == 1 {
		before := pixels.Bounds()
		if options.Mode == CompressionLossy {
			var err error
			pixels, err = pdfgo.ResizeImage(ctx, pixels, size)
			if err != nil {
				return nil, err
			}
		}
		density.x *= float64(pixels.Bounds().Dx()) / float64(before.Dx())
		density.y *= float64(pixels.Bounds().Dy()) / float64(before.Dy())
		size = image.Point{}
	}
	var encoded bytes.Buffer
	limit := &outputCompressionBuffer{buffer: &encoded, limit: outputOptimizationBufferLimit}
	if err := pdfgo.EncodePNG(ctx, limit, pixels); err != nil {
		if limit.exceeded {
			return data, ctx.Err()
		}
		return nil, err
	}
	input := encoded.Bytes()
	if hasDensity && density.unit == 1 {
		var err error
		input, err = pdfgo.OptimizeImageSize(ctx, input, options, size)
		if err != nil {
			return nil, err
		}
	}
	if hasDensity {
		x, y := math.Round(density.x), math.Round(density.y)
		if x > math.MaxInt32 || y > math.MaxInt32 || density.x > 0 && x < 1 || density.y > 0 && y < 1 || len(input) > outputOptimizationBufferLimit-21 {
			return data, ctx.Err()
		}
		chunk := make([]byte, 21)
		binary.BigEndian.PutUint32(chunk, 9)
		copy(chunk[4:], "pHYs")
		binary.BigEndian.PutUint32(chunk[8:], uint32(x))
		binary.BigEndian.PutUint32(chunk[12:], uint32(y))
		chunk[16] = density.unit
		binary.BigEndian.PutUint32(chunk[17:], crc32.ChecksumIEEE(chunk[4:17]))
		var output bytes.Buffer
		writer := imageMetadataWriter{writer: &output, skip: 33, data: chunk}
		if _, err := writer.Write(input); err != nil {
			return nil, err
		}
		input = output.Bytes()
	}
	candidate := input
	if !hasDensity || density.unit == 0 {
		var err error
		candidate, err = pdfgo.OptimizeImageResource(ctx, input, options, size)
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(candidate) < len(data) {
		return candidate, nil
	}
	return data, nil
}
