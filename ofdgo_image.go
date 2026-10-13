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
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"io"

	_ "github.com/xiaoqidun/j2kgo"
	_ "github.com/xiaoqidun/jbig2"
	_ "golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// transparentEdgeImage 共享只读像素并按需计算透明边缘和采样边距
type transparentEdgeImage struct {
	source image.Image
	rect   image.Rectangle
}

// transparentEdgeImage64 按需补齐十六位像素边缘，保留原始只读图像
type transparentEdgeImage64 struct {
	source image.Image
	rect   image.Rectangle
}

// Bounds 返回含采样边距的图像边界
// 返回: image.Rectangle 图像边界
func (s *transparentEdgeImage) Bounds() image.Rectangle { return s.rect }

// ColorModel 返回非预乘颜色模型
// 返回: color.Model 颜色模型
func (s *transparentEdgeImage) ColorModel() color.Model { return color.NRGBAModel }

// NRGBAAt 读取原始像素或按邻域补齐透明边缘，边界外透明
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA 非预乘颜色
func (s *transparentEdgeImage) NRGBAAt(x, y int) color.NRGBA {
	if !image.Pt(x, y).In(s.rect) {
		return color.NRGBA{}
	}
	b := s.source.Bounds()
	sx, sy := b.Min.X+min(max(x-1, 0), b.Dx()-1), b.Min.Y+min(max(y-1, 0), b.Dy()-1)
	c := imageNRGBAAt(s.source, sx, sy)
	if c.A == 0 {
		if edge, ok := transparentEdgeColor(s.source, sx, sy); ok {
			c = edge
		}
	}
	if x == 0 || y == 0 || x == s.rect.Max.X-1 || y == s.rect.Max.Y-1 {
		c = transparentPaddingColor(c)
	}
	return c
}

// At 返回非预乘像素颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 像素颜色
func (s *transparentEdgeImage) At(x, y int) color.Color { return s.NRGBAAt(x, y) }

// RGBA64At 返回预乘十六位像素，供采样器无装箱读取
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 预乘颜色
func (s *transparentEdgeImage) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.NRGBAAt(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// Bounds 返回含采样边距的图像边界
// 返回: image.Rectangle 图像边界
func (s *transparentEdgeImage64) Bounds() image.Rectangle { return s.rect }

// ColorModel 返回十六位非预乘颜色模型
// 返回: color.Model 颜色模型
func (s *transparentEdgeImage64) ColorModel() color.Model { return color.NRGBA64Model }

// NRGBA64At 读取十六位原始颜色，透明邻域使用最小非零透明度保留边缘颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.NRGBA64 非预乘颜色
func (s *transparentEdgeImage64) NRGBA64At(x, y int) color.NRGBA64 {
	if !image.Pt(x, y).In(s.rect) {
		return color.NRGBA64{}
	}
	b := s.source.Bounds()
	sx, sy := b.Min.X+min(max(x-1, 0), b.Dx()-1), b.Min.Y+min(max(y-1, 0), b.Dy()-1)
	c := imageNRGBA64At(s.source, sx, sy)
	if c.A == 0 {
		var best color.NRGBA64
		for ny := max(sy-1, b.Min.Y); ny < min(sy+2, b.Max.Y); ny++ {
			for nx := max(sx-1, b.Min.X); nx < min(sx+2, b.Max.X); nx++ {
				v := imageNRGBA64At(s.source, nx, ny)
				if v.A > best.A {
					best = v
				}
			}
		}
		if best.A != 0 {
			best.A = 1
			c = best
		}
	}
	if x == 0 || y == 0 || x == s.rect.Max.X-1 || y == s.rect.Max.Y-1 {
		c.A = 1
	}
	return c
}

// At 返回非预乘十六位像素颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.Color 像素颜色
func (s *transparentEdgeImage64) At(x, y int) color.Color { return s.NRGBA64At(x, y) }

// RGBA64At 返回保留原采样精度的预乘颜色
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 预乘颜色
func (s *transparentEdgeImage64) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.NRGBA64At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// decodeImageData 解码图片数据
// 入参: data 图片数据
// 返回: image.Image 图片对象, string 图片格式, error 错误信息
func decodeImageData(data []byte) (image.Image, string, error) {
	return decodeImageDataContext(context.Background(), data)
}

// decodeImageDataContext 解码图片并传递取消状态，PNG和JPEG仍保留惰性像素解码
// 入参: ctx 取消上下文, data 只读图片编码
// 返回: image.Image 图片对象, string 图片格式, error 解码或取消错误
func decodeImageDataContext(ctx context.Context, data []byte) (img image.Image, format string, err error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	defer func() {
		if cancelErr := ctx.Err(); cancelErr != nil {
			img, err = nil, cancelErr
		}
	}()
	if isJPEGData(data) || isPNGData(data) {
		img, err := newEncodedImageContext(ctx, data)
		if err != nil {
			return nil, "", err
		}
		return img, img.format, nil
	}
	if isTIFFData(data) {
		layout, err := readTIFFLayoutContext(ctx, bytes.NewReader(data))
		if err != nil {
			return nil, "tiff", err
		}
		img, err := decodeTIFFImage(ctx, data, layout)
		return img, "tiff", err
	}
	img, format, err = image.Decode(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
	if err == nil {
		return img, normalizeSealType(format), nil
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if img, err := decodeBMPImageContext(ctx, data); err == nil {
		return img, "bmp", nil
	}
	return nil, "", err
}

// decodeImagePixelsContext 完整解码图片，确保像素可读取并传递取消状态
// 入参: ctx 取消上下文, data 图片编码数据
// 返回: image.Image 解码像素, string 图片格式, error 解码或取消错误
func decodeImagePixelsContext(ctx context.Context, data []byte) (image.Image, string, error) {
	img, format, err := decodeImageDataContext(ctx, data)
	if err != nil {
		return nil, format, err
	}
	pixels, err := imagePixelDataContext(ctx, img)
	return pixels, format, err
}

// isJPEGData 判断是否为JPEG图片数据
// 入参: data 图片数据
// 返回: bool 是否为JPEG图片数据
func isJPEGData(data []byte) bool {
	return len(data) >= 2 && data[0] == 0xFF && data[1] == 0xD8
}

// isPNGData 判断是否为PNG图片数据
// 入参: data 图片数据
// 返回: bool 是否为PNG图片数据
func isPNGData(data []byte) bool {
	return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
}

// decodeImageConfigData 解码图片尺寸
// 入参: data 图片数据
// 返回: image.Config 图片尺寸, string 图片格式, error 错误信息
func decodeImageConfigData(data []byte) (image.Config, string, error) {
	return decodeImageConfigDataContext(context.Background(), data)
}

// decodeImageConfigDataContext 读取图片尺寸和颜色模型，传递布局解析中的取消状态
// 入参: ctx 取消上下文, data 图片数据
// 返回: image.Config 图片配置, string 图片格式, error 读取或取消错误
func decodeImageConfigDataContext(ctx context.Context, data []byte) (config image.Config, format string, err error) {
	if err := ctx.Err(); err != nil {
		return image.Config{}, "", err
	}
	defer func() {
		if cancelErr := ctx.Err(); cancelErr != nil {
			config, err = image.Config{}, cancelErr
		}
	}()
	if isTIFFData(data) {
		layout, err := readTIFFLayoutContext(ctx, bytes.NewReader(data))
		if err != nil {
			return image.Config{}, "tiff", err
		}
		profile, err := readTIFFColorProfile(ctx, data, layout)
		if err != nil {
			return image.Config{}, "tiff", err
		}
		var cfg image.Config
		if layout.needsSampleView() {
			img, _, err := tiffPlaneDirectories(ctx, data, layout)
			if err != nil {
				return image.Config{}, "tiff", err
			}
			cfg.ColorModel = img.ColorModel()
		} else {
			input := newTIFFDirectoryInput(data, layout.offset)
			input.ctx = ctx
			cfg, err = tiff.DecodeConfig(input)
			if err != nil {
				return image.Config{}, "tiff", err
			}
		}
		if profile != nil || layout.transparency != nil {
			cfg.ColorModel = color.NRGBA64Model
		}
		if layout.transparency != nil {
			if _, err := readTIFFColorProfile(ctx, data, *layout.transparency); err != nil {
				return image.Config{}, "tiff", err
			}
			if _, _, err := tiffPlaneDirectories(ctx, data, *layout.transparency); err != nil {
				return image.Config{}, "tiff", err
			}
		}
		size := layout.displaySize()
		cfg.Width, cfg.Height = size.X, size.Y
		return cfg, "tiff", nil
	}
	cfg, format, err := image.DecodeConfig(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
	if err == nil {
		return cfg, normalizeSealType(format), nil
	}
	if cfg, err := decodeBMPConfig(data); err == nil {
		return cfg, "bmp", nil
	}
	return image.Config{}, "", err
}

// decodeBMPConfig 解码BMP尺寸
// 入参: data 图片数据
// 返回: image.Config 图片尺寸, error 错误信息
func decodeBMPConfig(data []byte) (image.Config, error) {
	offset, width, height, bpp, compression, err := parseBMPHeader(data)
	if err != nil {
		return image.Config{}, err
	}
	if offset <= 0 || width <= 0 || height == 0 {
		return image.Config{}, fmt.Errorf("unsupported bmp")
	}
	if _, err := bmpColorMasks(data, offset, bpp, compression); err != nil {
		return image.Config{}, err
	}
	if height < 0 {
		height = -height
	}
	return image.Config{ColorModel: color.NRGBAModel, Width: width, Height: height}, nil
}

// decodeBMPImage 解码BMP图片
// 入参: data 图片数据
// 返回: image.Image 图片对象, error 错误信息
func decodeBMPImage(data []byte) (image.Image, error) {
	return decodeBMPImageContext(context.Background(), data)
}

// decodeBMPImageContext 解码BMP真彩色像素，逐行检查取消
// 入参: ctx 取消上下文, data 图片数据
// 返回: image.Image 图片对象, error 格式或取消错误
func decodeBMPImageContext(ctx context.Context, data []byte) (image.Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	offset, width, height, bpp, compression, err := parseBMPHeader(data)
	if err != nil {
		return nil, err
	}
	if offset > len(data) {
		return nil, fmt.Errorf("invalid bmp pixel offset")
	}
	masks, err := bmpColorMasks(data, offset, bpp, compression)
	if err != nil {
		return nil, err
	}
	topDown := height < 0
	if topDown {
		height = -height
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid bmp size")
	}
	stride := ((int64(width)*int64(bpp) + 31) / 32) * 4
	if stride > int64(len(data)-offset)/int64(height) {
		return nil, fmt.Errorf("truncated bmp")
	}
	if width > int(^uint(0)>>1)/4/height {
		return nil, fmt.Errorf("bmp dimensions exceed image capacity")
	}
	rowStride := int(stride)
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		row := data[offset+srcY*rowStride:]
		for x := 0; x < width; x++ {
			switch bpp {
			case 16, 32:
				var value uint32
				if bpp == 16 {
					value = uint32(binary.LittleEndian.Uint16(row[x*2:]))
				} else {
					value = binary.LittleEndian.Uint32(row[x*4:])
				}
				p := y*img.Stride + x*4
				for i, mask := range masks {
					img.Pix[p+i] = mask.sample(value)
				}
			case 24:
				p := x * 3
				img.SetNRGBA(x, y, color.NRGBA{R: row[p+2], G: row[p+1], B: row[p], A: 255})
			}
		}
	}
	return img, nil
}

// parseBMPHeader 解析BMP文件头
// 入参: data 图片数据
// 返回: int 像素偏移, int 宽度, int 高度, uint16 位数, uint32 压缩方式, error 错误信息
func parseBMPHeader(data []byte) (int, int, int, uint16, uint32, error) {
	if len(data) < 54 || string(data[:2]) != "BM" {
		return 0, 0, 0, 0, 0, fmt.Errorf("not bmp")
	}
	offset := int(binary.LittleEndian.Uint32(data[10:14]))
	dibSize := binary.LittleEndian.Uint32(data[14:18])
	if dibSize < 40 {
		return 0, 0, 0, 0, 0, fmt.Errorf("unsupported bmp")
	}
	if uint64(dibSize)+14 > uint64(len(data)) || offset < 0 || uint64(offset) < uint64(dibSize)+14 {
		return 0, 0, 0, 0, 0, fmt.Errorf("invalid bmp pixel offset")
	}
	width := int(int32(binary.LittleEndian.Uint32(data[18:22])))
	height := int(int32(binary.LittleEndian.Uint32(data[22:26])))
	planes := binary.LittleEndian.Uint16(data[26:28])
	bpp := binary.LittleEndian.Uint16(data[28:30])
	compression := binary.LittleEndian.Uint32(data[30:34])
	if planes != 1 || width <= 0 || height == 0 || int64(height) == -int64(^uint(0)>>1)-1 {
		return 0, 0, 0, 0, 0, fmt.Errorf("invalid bmp")
	}
	return offset, width, height, bpp, compression, nil
}

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
	source, err := imagePixelDataContext(ctx, img)
	if err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	var maskBounds image.Rectangle
	if mask != nil {
		mask, err = imagePixelDataContext(ctx, mask)
		if err != nil {
			return nil, err
		}
		maskBounds = mask.Bounds()
		if bounds.Empty() || bounds.Size() != maskBounds.Size() {
			return nil, fmt.Errorf("image mask dimensions differ from source")
		}
	}
	if imageHighPrecision(source) {
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
				pixel := imageNRGBA64At(source, x, y)
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
				out.SetNRGBA(x, y, imageNRGBAAt(source, x, y))
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
	mask, err := imagePixelDataContext(ctx, mask)
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

// imagePixelSource 获取图片像素源
// 入参: img 图片对象
// 返回: image.Image 图片像素源
func imagePixelSource(img image.Image) image.Image {
	if decoded, err := imagePixelData(img); err == nil {
		return decoded
	}
	return img
}

// imagePixelData 获取像素并保留惰性解码错误，供绘制和像素处理入口校验
// 入参: img 图片对象
// 返回: image.Image 解码像素, error 解码错误
func imagePixelData(img image.Image) (image.Image, error) {
	if src, ok := img.(interface{ Image() (image.Image, error) }); ok {
		return src.Image()
	}
	return img, nil
}

// imagePixelDataContext 获取像素并传递取消状态，兼容不接收上下文的图像实现
// 入参: ctx 取消上下文，nil使用后台上下文, img 图片对象
// 返回: image.Image 解码像素, error 解码或取消错误
func imagePixelDataContext(ctx context.Context, img image.Image) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if src, ok := img.(interface {
		ImageContext(context.Context) (image.Image, error)
	}); ok {
		return src.ImageContext(ctx)
	}
	pixels, err := imagePixelData(img)
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	return pixels, err
}

// imageGrayAt 获取图片灰度像素
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.Gray 灰度像素
func imageGrayAt(img image.Image, x, y int) color.Gray {
	if src, ok := img.(*image.Gray); ok {
		return src.GrayAt(x, y)
	}
	if src, ok := img.(*image.Paletted); ok {
		return color.GrayModel.Convert(src.At(x, y)).(color.Gray)
	}
	if src, ok := img.(image.RGBA64Image); ok {
		r, g, b, _ := src.RGBA64At(x, y).RGBA()
		return color.Gray{Y: uint8((19595*r + 38470*g + 7471*b + 1<<15) >> 24)}
	}
	return color.GrayModel.Convert(img.At(x, y)).(color.Gray)
}

// imageNRGBAAt 读取八位非预乘像素，缩减位深时保留原始颜色分量
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.NRGBA NRGBA像素
func imageNRGBAAt(img image.Image, x, y int) color.NRGBA {
	switch src := img.(type) {
	case interface{ NRGBAAt(int, int) color.NRGBA }:
		return src.NRGBAAt(x, y)
	case interface{ NRGBA64At(int, int) color.NRGBA64 }:
		c := src.NRGBA64At(x, y)
		return color.NRGBA{R: uint8(c.R >> 8), G: uint8(c.G >> 8), B: uint8(c.B >> 8), A: uint8(c.A >> 8)}
	case *image.RGBA, *image.RGBA64, *image.Gray, *image.Gray16, *image.Alpha, *image.Alpha16, *image.CMYK, *image.YCbCr, *image.NYCbCrA:
		c := src.(image.RGBA64Image).RGBA64At(x, y)
		if c.A == 0 {
			return color.NRGBA{}
		}
		r, g, b, a := uint32(c.R), uint32(c.G), uint32(c.B), uint32(c.A)
		if a != 0xffff {
			r = r * 0xffff / a
			g = g * 0xffff / a
			b = b * 0xffff / a
		}
		return color.NRGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
	}
	value := img.At(x, y)
	if c, ok := value.(color.NRGBA64); ok {
		return color.NRGBA{R: uint8(c.R >> 8), G: uint8(c.G >> 8), B: uint8(c.B >> 8), A: uint8(c.A >> 8)}
	}
	return color.NRGBAModel.Convert(value).(color.NRGBA)
}

// imageAlphaAt 读取十六位透明度，原生图像避免颜色装箱
// 入参: img 图像, x 横坐标, y 纵坐标
// 返回: uint32 十六位透明度
func imageAlphaAt(img image.Image, x, y int) uint32 {
	if source, ok := img.(image.RGBA64Image); ok {
		return uint32(source.RGBA64At(x, y).A)
	}
	_, _, _, alpha := img.At(x, y).RGBA()
	return alpha
}

// imageHighPrecision 判断颜色模型及调色板是否需要保留十六位采样
// 入参: img 图片对象
// 返回: bool 是否使用十六位颜色或透明度
func imageHighPrecision(img image.Image) bool {
	model := img.ColorModel()
	if palette, ok := model.(color.Palette); ok {
		for _, value := range palette {
			switch value.(type) {
			case color.RGBA, color.NRGBA, color.Gray, color.Alpha, color.CMYK, color.YCbCr, color.NYCbCrA:
			default:
				pixel := color.NRGBA64Model.Convert(value).(color.NRGBA64)
				if pixel.R%257 != 0 || pixel.G%257 != 0 || pixel.B%257 != 0 || pixel.A%257 != 0 {
					return true
				}
			}
		}
		return false
	}
	return model == color.NRGBA64Model || model == color.RGBA64Model || model == color.Gray16Model || model == color.Alpha16Model
}

// imageNRGBA64At 读取十六位非预乘像素，保留透明像素颜色，原生图像避免颜色装箱
// 入参: img 图片对象, x X坐标, y Y坐标
// 返回: color.NRGBA64 NRGBA64像素
func imageNRGBA64At(img image.Image, x, y int) color.NRGBA64 {
	switch src := img.(type) {
	case interface{ NRGBA64At(int, int) color.NRGBA64 }:
		return src.NRGBA64At(x, y)
	case *image.NRGBA:
		c := src.NRGBAAt(x, y)
		return color.NRGBA64{R: uint16(c.R) * 257, G: uint16(c.G) * 257, B: uint16(c.B) * 257, A: uint16(c.A) * 257}
	case *image.Gray:
		v := uint16(src.GrayAt(x, y).Y) * 257
		return color.NRGBA64{R: v, G: v, B: v, A: 0xffff}
	case *image.Gray16:
		c := src.Gray16At(x, y)
		return color.NRGBA64{R: c.Y, G: c.Y, B: c.Y, A: 0xffff}
	case *image.Alpha, *image.Alpha16:
		c := src.(image.RGBA64Image).RGBA64At(x, y)
		if c.A == 0 {
			return color.NRGBA64{}
		}
		return color.NRGBA64{R: 0xffff, G: 0xffff, B: 0xffff, A: c.A}
	case *image.RGBA, *image.RGBA64, *image.CMYK, *image.YCbCr, *image.NYCbCrA:
		c := src.(image.RGBA64Image).RGBA64At(x, y)
		if c.A == 0 {
			return color.NRGBA64{}
		}
		r, g, b, a := uint32(c.R), uint32(c.G), uint32(c.B), uint32(c.A)
		if a != 0xffff {
			r = r * 0xffff / a
			g = g * 0xffff / a
			b = b * 0xffff / a
		}
		return color.NRGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: c.A}
	}
	value := img.At(x, y)
	if c, ok := value.(color.NRGBA); ok {
		return color.NRGBA64{R: uint16(c.R) * 257, G: uint16(c.G) * 257, B: uint16(c.B) * 257, A: uint16(c.A) * 257}
	}
	return color.NRGBA64Model.Convert(value).(color.NRGBA64)
}

// transparentEdgeColor 获取透明像素相邻的可见颜色
// 入参: source 图片对象, x X坐标, y Y坐标
// 返回: color.NRGBA 颜色, bool 是否存在
func transparentEdgeColor(source image.Image, x, y int) (color.NRGBA, bool) {
	img, ok := source.(*image.NRGBA)
	if !ok {
		bounds := source.Bounds()
		var best color.NRGBA
		for ny := max(y-1, bounds.Min.Y); ny < min(y+2, bounds.Max.Y); ny++ {
			for nx := max(x-1, bounds.Min.X); nx < min(x+2, bounds.Max.X); nx++ {
				if nx == x && ny == y {
					continue
				}
				pixel := imageNRGBAAt(source, nx, ny)
				if pixel.A > best.A {
					best = pixel
					if best.A == 255 {
						best.A = 1
						return best, true
					}
				}
			}
		}
		if best.A == 0 {
			return color.NRGBA{}, false
		}
		best.A = 1
		return best, true
	}
	bounds := img.Bounds()
	best, alpha := 0, uint8(0)
	left, right := max(x-1, bounds.Min.X), min(x+2, bounds.Max.X)
scan:
	for ny := max(y-1, bounds.Min.Y); ny < min(y+2, bounds.Max.Y); ny++ {
		offset := img.PixOffset(left, ny)
		for nx := left; nx < right; nx++ {
			if (nx != x || ny != y) && img.Pix[offset+3] > alpha {
				best, alpha = offset, img.Pix[offset+3]
				if alpha == 255 {
					break scan
				}
			}
			offset += 4
		}
	}
	if alpha == 0 {
		return color.NRGBA{}, false
	}
	return color.NRGBA{R: img.Pix[best], G: img.Pix[best+1], B: img.Pix[best+2], A: 1}, true
}

// transparentPaddingColor 获取透明补齐颜色
// 入参: c 边缘颜色
// 返回: color.NRGBA 补齐颜色
func transparentPaddingColor(c color.NRGBA) color.NRGBA {
	c.A = 1
	return c
}

// imageWithTransparentEdge 扫描透明边缘并共享只读像素，保留原图采样精度
// 入参: ctx 取消上下文，nil使用后台上下文, img 图片对象
// 返回: image.Image 补边图像, int 补边像素数, error 解码或取消错误
func imageWithTransparentEdge(ctx context.Context, img image.Image) (image.Image, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if img == nil {
		return img, 0, nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return img, 0, nil
	}
	if src, ok := img.(*EncodedImage); ok && src.MIME() == "image/jpeg" {
		return img, 0, nil
	}
	source, err := imagePixelDataContext(ctx, img)
	if err != nil {
		return nil, 0, err
	}
	switch source.(type) {
	case *image.Gray, *image.Gray16, *image.YCbCr, *image.CMYK:
		return img, 0, nil
	}
	hasZero, hasVisible := false, false
	var pixels []byte
	var stride, step int
	switch src := source.(type) {
	case *image.NRGBA:
		pixels, stride, step = src.Pix, src.Stride, 4
	case *image.RGBA:
		pixels, stride, step = src.Pix, src.Stride, 4
	case *image.Alpha:
		pixels, stride, step = src.Pix, src.Stride, 1
	}
	if pixels != nil {
		hasZero = pixels[step-1] == 0
		hasVisible = !hasZero
	scan:
		for y := 0; y < h; y++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			offset := y*stride + step - 1
			for x := 0; x < w; x++ {
				if x&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, 0, err
					}
				}
				if (pixels[offset] == 0) != hasZero {
					hasZero, hasVisible = true, true
					break scan
				}
				offset += step
			}
		}
	} else {
		for y := bounds.Min.Y; y < bounds.Max.Y && !(hasZero && hasVisible); y++ {
			if err := ctx.Err(); err != nil {
				return nil, 0, err
			}
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&255 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, 0, err
					}
				}
				if imageAlphaAt(source, x, y) == 0 {
					hasZero = true
				} else {
					hasVisible = true
				}
				if hasZero && hasVisible {
					break
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if !hasZero || !hasVisible {
		return img, 0, nil
	}
	if imageHighPrecision(source) {
		return &transparentEdgeImage64{source: source, rect: image.Rect(0, 0, w+2, h+2)}, 1, nil
	}
	return &transparentEdgeImage{source: source, rect: image.Rect(0, 0, w+2, h+2)}, 1, nil
}
