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
	"errors"
	"image"
	"io"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"github.com/xiaoqidun/pdfgo"
)

// outputImageFormat 识别可由资源压缩生成的图片格式
// 入参: data 图片编码
// 返回: string 标准资源格式，未识别时为空
func outputImageFormat(data []byte) string {
	if baseimage.IsJPEG(data) {
		return "JPEG"
	}
	if baseimage.IsPNG(data) {
		return "PNG"
	}
	return ""
}

// optimizeImageResource 优化可完整保留的BMP及TIFF，复用图像压缩和降采样策略
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
	if options.Mode != CompressionUnchanged && baseimage.IsTIFF(data) {
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
	offset, width, height, depth, compression, err := baseimage.ParseBMPHeader(data)
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
	density, ok := baseimage.BMPDensity(data)
	if !ok {
		return data, nil
	}
	if depth == 16 || depth == 32 {
		masks, err := baseimage.BMPColorMasks(data, offset, depth, compression)
		if err != nil {
			return data, nil
		}
		if options.Mode == CompressionLossless {
			for _, mask := range masks {
				if mask.Max > 255 {
					return data, nil
				}
			}
		}
	}
	var pixels image.Image
	if depth == 16 || compression == 3 {
		pixels, err = baseimage.DecodeBMPContext(ctx, data)
	} else {
		pixels, _, err = image.Decode(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), ctx: ctx})
	}
	if err != nil {
		return nil, err
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, density)
}

// optimizeRasterResource 编码已解码的资源图片，同步降采样后的像素密度，仅采用更小结果
// 先降采样再编码，绝对密度按最终尺寸量化，相对密度保留精确比例
// 有密度时保留PNG，未知密度按最终像素选择编码格式
// 入参: ctx 取消上下文, data 原编码, pixels 像素, options 压缩选项, size 像素需求, density 像素密度
// 返回: []byte 图片编码, error 编码或取消错误
func optimizeRasterResource(ctx context.Context, data []byte, pixels image.Image, options CompressionOptions, size image.Point, density baseimage.Density) ([]byte, error) {
	hasDensity := density.X != 0 || density.Y != 0
	before := pixels.Bounds().Size()
	if options.Mode == CompressionLossy {
		var err error
		pixels, err = pdfgo.ResizeImage(ctx, pixels, size)
		if err != nil {
			return nil, err
		}
	}
	var ok bool
	density, ok = density.Scale(before, pixels.Bounds().Size())
	if !ok {
		return data, ctx.Err()
	}
	var candidate []byte
	var err error
	if hasDensity {
		var encoded bytes.Buffer
		err = pdfgo.EncodePNGWithOptions(ctx, &encoded, pixels, options)
		candidate = encoded.Bytes()
	} else {
		candidate, err = pdfgo.EncodeImageResource(ctx, pixels, options)
	}
	if err != nil {
		if errors.Is(err, io.ErrShortBuffer) {
			return data, ctx.Err()
		}
		return nil, err
	}
	if hasDensity {
		if len(candidate) > outputOptimizationBufferLimit-baseimage.PNGDensityChunkSize {
			return data, ctx.Err()
		}
		candidate, ok = baseimage.WithPNGDensity(candidate, density)
		if !ok {
			return data, ctx.Err()
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

// optimizeTIFFResource 压缩单目录TIFF，保留方向、透明度、采样精度及像素密度
// 主图或单个瓦片的预计缓冲超限时保留原编码
// 入参: ctx 取消上下文, data TIFF编码, options 压缩选项, size 显示像素需求
// 返回: []byte 图片编码，不可完整转换时保留原文, error 编码或取消错误
func optimizeTIFFResource(ctx context.Context, data []byte, options CompressionOptions, size image.Point) ([]byte, error) {
	layout, err := baseimage.ReadTIFFLayoutContext(ctx, bytes.NewReader(data))
	if err != nil {
		return data, ctx.Err()
	}
	pixelSize := int64(layout.DecodedPixelBytes())
	if int64(layout.Width) > outputOptimizationBufferLimit/pixelSize/int64(layout.Height) {
		return data, ctx.Err()
	}
	if layout.TileWidth != 0 && int64(layout.TileWidth) > outputOptimizationBufferLimit/pixelSize/int64(layout.TileHeight) {
		return data, ctx.Err()
	}
	density, ok := baseimage.TIFFConversionDensity(data, layout)
	if !ok {
		return data, ctx.Err()
	}
	pixels, err := baseimage.DecodeTIFF(ctx, data, layout)
	if err != nil {
		return data, ctx.Err()
	}
	return optimizeRasterResource(ctx, data, pixels, options, size, density)
}
