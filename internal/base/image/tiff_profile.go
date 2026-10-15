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

package image

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// parseTIFFColorProfile 校验ICC源模型，保留原有模型错误并复用浮点颜色变换
// 入参: kind 源颜色模型, data ICC编码
// 返回: *pdfgo.ColorSpace 颜色变换, error 模型或配置错误
func parseTIFFColorProfile(kind string, data []byte) (*pdfgo.ColorSpace, error) {
	model := map[string]string{"GRAY": "GRAY", "RGB": "RGB ", "CMYK": "CMYK"}[strings.ToUpper(kind)]
	if len(data) < 132 || model == "" || string(data[16:20]) != model {
		return nil, fmt.Errorf("ICC source model does not match image color space %q", kind)
	}
	return pdfgo.NewICCColorSpace(data)
}

// readTIFFColorProfile 读取主图的ICC配置，校验颜色模型和数据范围
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 主图布局
// 返回: *pdfgo.ColorSpace 颜色配置，未声明时为nil, error 配置或取消错误
func readTIFFColorProfile(ctx context.Context, data []byte, layout TIFFLayout) (*pdfgo.ColorSpace, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if layout.profileSize == 0 {
		return nil, nil
	}
	at, size := uint64(layout.profileOffset), uint64(layout.profileSize)
	if at+size > uint64(len(data)) {
		return nil, fmt.Errorf("truncated TIFF color profile")
	}
	kind := ""
	switch layout.photometric {
	case 0, 1:
		kind = "GRAY"
	case 2, 3:
		kind = "RGB"
	case 5:
		kind = "CMYK"
	default:
		return nil, fmt.Errorf("unsupported TIFF profile color model")
	}
	profile, err := parseTIFFColorProfile(kind, data[at:at+size])
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	return profile, err
}

// applyTIFFColorProfile 将源颜色转换为十六位sRGB，保留独立透明度和隐藏颜色
// 入参: ctx 取消上下文，可为nil, source 解码图像, profile 已校验的颜色配置
// 返回: image.Image 独立透明度图像, error 颜色变换或取消错误
func applyTIFFColorProfile(ctx context.Context, source image.Image, profile *pdfgo.ColorSpace) (image.Image, error) {
	return applyImageColorProfile(ctx, source, profile, false)
}

// applyImageColorProfile 按源配置转换显示像素，精确复用连续同色结果
// 入参: ctx 取消上下文, source 源像素, profile 颜色空间, linearSRGB 是否先解码sRGB传递函数
// 返回: image.Image 十六位显示图像, error 变换或取消错误
func applyImageColorProfile(ctx context.Context, source image.Image, profile *pdfgo.ColorSpace, linearSRGB bool) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bounds := source.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 || bounds.Dx() > math.MaxInt/8/bounds.Dy() {
		return nil, fmt.Errorf("TIFF profile image dimensions exceed capacity")
	}
	cmyk, _ := source.(*planarTIFFImage)
	if cmyk != nil && !cmyk.cmyk {
		cmyk = nil
	}
	model := source.ColorModel()
	premultiplied := model == color.RGBAModel || model == color.RGBA64Model
	table := newTIFFProfileTable(source)
	result := image.NewNRGBA64(bounds)
	var previous [4]float64
	var previousPixel color.NRGBA64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		misses := 0
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if (x-bounds.Min.X)&255 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			slot := -1
			if len(table) != 0 {
				index, alpha := tiffProfileSample(source, x, y, len(table))
				slot = index
				if pixel := table[index]; pixel.A != 0 {
					pixel.A = alpha
					result.SetNRGBA64(x, y, pixel)
					continue
				}
			}
			var values [4]float64
			var alpha uint16
			if cmyk != nil {
				ink := cmyk.inksAt(x, y)
				alpha = ink[4]
				if cmyk.alpha == 1 && alpha == 0 {
					continue
				}
				maximum := float64(65535)
				if cmyk.alpha == 1 {
					maximum = float64(alpha)
				}
				for i := range values {
					values[i] = min(1, float64(ink[i])/maximum)
				}
			} else if premultiplied {
				var pixel color.RGBA64
				if native, ok := source.(image.RGBA64Image); ok {
					pixel = native.RGBA64At(x, y)
				} else {
					pixel = color.RGBA64Model.Convert(source.At(x, y)).(color.RGBA64)
				}
				alpha = pixel.A
				if alpha == 0 {
					continue
				}
				for i, value := range [3]uint16{pixel.R, pixel.G, pixel.B} {
					values[i] = min(1, float64(value)/float64(alpha))
				}
			} else {
				pixel := NRGBA64At(source, x, y)
				alpha = pixel.A
				values = [4]float64{float64(pixel.R) / 65535, float64(pixel.G) / 65535, float64(pixel.B) / 65535}
			}
			if misses < 32 {
				if previousPixel.A != 0 && values == previous {
					pixel := previousPixel
					pixel.A = alpha
					result.SetNRGBA64(x, y, pixel)
					misses = 0
					continue
				}
				previous = values
				misses++
			}
			if linearSRGB {
				for i := range 3 {
					if values[i] <= 0.04045 {
						values[i] /= 12.92
					} else {
						values[i] = math.Pow((values[i]+0.055)/1.055, 2.4)
					}
				}
			}
			rgb, err := profile.SRGB(values[:profile.Components()], profile.DefaultIntent())
			if err != nil {
				return nil, err
			}
			var channels [3]uint16
			for i, value := range rgb {
				channels[i] = uint16(math.Round(max(0, min(1, value)) * 65535))
			}
			if misses < 32 {
				previousPixel = color.NRGBA64{R: channels[0], G: channels[1], B: channels[2], A: 65535}
			} else {
				previousPixel.A = 0
			}
			if slot >= 0 {
				table[slot] = color.NRGBA64{R: channels[0], G: channels[1], B: channels[2], A: 65535}
			}
			result.SetNRGBA64(x, y, color.NRGBA64{R: channels[0], G: channels[1], B: channels[2], A: alpha})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &colorImage{NRGBA64: result, source: source}, nil
}

// newTIFFProfileTable 为离散颜色分配查找表，预乘颜色及小图保持逐像素变换
// 入参: source 已解码且尺寸有效的图像
// 返回: []color.NRGBA64 颜色表，Alpha非零表示已有结果，不适用时为nil
func newTIFFProfileTable(source image.Image) []color.NRGBA64 {
	count := 0
	switch source := source.(type) {
	case *image.Gray:
		count = 256
	case *image.Gray16:
		count = 65536
	case *image.Paletted:
		count = len(source.Palette)
	case *planarTIFFImage:
		if source.alpha != 1 {
			if len(source.palette) != 0 {
				count = len(source.palette)
			} else if source.gray {
				count = 256
				if source.packed[0] != 0 {
					count = 1 << source.packed[0]
				} else if _, ok := source.planes[0].(*image.Gray16); ok {
					count = 65536
				}
			}
		}
	}
	if count == 0 || count >= source.Bounds().Dx()*source.Bounds().Dy() {
		return nil
	}
	return make([]color.NRGBA64, count)
}

// tiffProfileSample 读取查找表索引与独立透明度，颜色键不包含透明度
// 入参: source 适用查找表的图像, x 横坐标, y 纵坐标, count 颜色表长度
// 返回: int 颜色索引, uint16 透明度
func tiffProfileSample(source image.Image, x, y, count int) (int, uint16) {
	switch source := source.(type) {
	case *image.Gray:
		return int(source.GrayAt(x, y).Y), 65535
	case *image.Gray16:
		return int(source.Gray16At(x, y).Y), 65535
	case *image.Paletted:
		index := source.ColorIndexAt(x, y)
		_, _, _, alpha := source.Palette[index].RGBA()
		return int(index), uint16(alpha)
	case *planarTIFFImage:
		index := int(uint32(source.sampleAt(0, x, y)) * uint32(count-1) / 65535)
		alpha := uint16(65535)
		if source.alpha != 0 {
			alpha = source.sampleAt(1, x, y)
		}
		return index, alpha
	}
	return 0, 0
}
