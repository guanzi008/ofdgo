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
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"

	baseimage "github.com/xiaoqidun/ofdgo/internal/base/image"
	"golang.org/x/image/tiff"
)

// U3DTextureResolver 由调用方按原始URL读取外部纹理，地址解析、访问策略和读取限额由调用方控制
// 返回的数据在本次绘制结束前不得修改；失败时尝试下一候选地址，取消及超时错误立即返回
// 入参: ctx 取消上下文, url 绝对或相对地址
// 返回: []byte 压缩图像数据, error 读取错误
type U3DTextureResolver func(ctx context.Context, url string) ([]byte, error)

// u3dImageReader 在图像解码器读取压缩数据时检查取消状态
type u3dImageReader struct {
	ctx  context.Context
	data *bytes.Reader
}

// Read 读取顺序图像数据
// 入参: p 接收缓冲
// 返回: int 已读字节数, error 读取或取消错误
func (r *u3dImageReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.data.Read(p)
}

// ReadAt 读取指定偏移的图像数据
// 入参: p 接收缓冲, offset 字节偏移
// 返回: int 已读字节数, error 读取或取消错误
func (r *u3dImageReader) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.data.ReadAt(p, offset)
}

// texture 按需合成纹理并缓存，空名称使用ECMA-363第8.4.1节规定的默认纹理
// 入参: name 纹理名称
// 返回: *image.NRGBA64 非预乘纹理, error 资源、解码、预算或取消错误
func (r *u3dRender) texture(name string) (*image.NRGBA64, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if cached := r.textures[name]; cached != nil {
		return cached, nil
	}
	if err := r.reserve(1, 256); err != nil {
		return nil, err
	}
	var pixels *image.NRGBA64
	if name == "" {
		if err := r.reserve(64, 8); err != nil {
			return nil, err
		}
		pixels = image.NewNRGBA64(image.Rect(0, 0, 8, 8))
		for y := range 8 {
			for x := range 8 {
				c := color.NRGBA64{65535, 65535, 65535, 65535}
				if x/4 != y/4 {
					c.G, c.B = 26214, 13107
				}
				pixels.SetNRGBA64(x, y, c)
			}
		}
	} else {
		index, exists := r.textureIndex[name]
		if !exists {
			return nil, fmt.Errorf("missing U3D texture %q", name)
		}
		var err error
		pixels, err = r.decodeTexture(&r.model.Textures[index])
		if err != nil {
			return nil, fmt.Errorf("U3D texture %q: %w", name, err)
		}
	}
	if r.textures == nil {
		r.textures = make(map[string]*image.NRGBA64)
	}
	r.textures[name] = pixels
	return pixels, nil
}

// decodeTexture 合成各续图像指定的通道，保留十六位颜色与非预乘透明度
// 入参: texture 纹理资源
// 返回: *image.NRGBA64 合成图像, error 格式、预算或取消错误
func (r *u3dRender) decodeTexture(texture *U3DTexture) (*image.NRGBA64, error) {
	switch texture.ImageType {
	case 1, 14, 15, 16, 17:
	default:
		return nil, fmt.Errorf("invalid image type")
	}
	if texture.Width == 0 || texture.Height == 0 || len(texture.Images) == 0 || len(texture.Images) > 4 {
		return nil, fmt.Errorf("invalid image dimensions or channel count")
	}
	var covered uint8
	for _, source := range texture.Images {
		if source.Compression < 1 || source.Compression > 4 {
			return nil, fmt.Errorf("invalid image compression")
		}
		channels := u3dTextureChannels(source.Channels)
		if source.Channels == 0 || source.Channels&^uint8(31) != 0 || channels&covered != 0 || channels&^u3dTextureChannels(texture.ImageType) != 0 {
			return nil, fmt.Errorf("invalid image channels")
		}
		covered |= channels
		if source.External && (r.resolveTexture == nil || len(source.URLs) == 0) {
			return nil, fmt.Errorf("external texture image requires resolution")
		}
		if !source.External && uint64(len(source.Data)) != uint64(source.ByteCount) {
			return nil, fmt.Errorf("invalid image data length")
		}
	}
	if covered != u3dTextureChannels(texture.ImageType) {
		return nil, fmt.Errorf("incomplete image channels")
	}
	if err := r.reserve(uint64(texture.Width)*uint64(texture.Height), 8); err != nil {
		return nil, err
	}
	pixels := image.NewNRGBA64(image.Rect(0, 0, int(texture.Width), int(texture.Height)))
	for y := range pixels.Rect.Dy() {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		for x := range pixels.Rect.Dx() {
			if x%4096 == 0 {
				if err := r.ctx.Err(); err != nil {
					return nil, err
				}
			}
			pixels.SetNRGBA64(x, y, color.NRGBA64{65535, 65535, 65535, 65535})
		}
	}
	for _, source := range texture.Images {
		if err := r.textureSource(pixels, source); err != nil {
			return nil, err
		}
	}
	return pixels, r.ctx.Err()
}

// textureSource 读取内置通道或按顺序尝试外部地址，不修改模型中的图像声明
// 入参: pixels 目标纹理, source 通道图像
// 返回: error 读取、解码、预算或取消错误
func (r *u3dRender) textureSource(pixels *image.NRGBA64, source U3DTextureImage) error {
	if !source.External {
		return r.textureChannels(pixels, source)
	}
	if r.resolveTexture == nil || len(source.URLs) == 0 {
		return fmt.Errorf("external texture image requires resolution")
	}
	var last error
	for _, address := range source.URLs {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		data, err := r.resolveTexture(r.ctx, address)
		if r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		if err == nil {
			source.Data = data
			err = r.textureChannels(pixels, source)
			source.Data = nil
		}
		if canceled := r.ctx.Err(); canceled != nil {
			return canceled
		}
		if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		last = fmt.Errorf("external texture image %q: %w", address, err)
	}
	return last
}

// textureChannels 解码并合成一幅续图像，临时预算包括像素、分块及压缩数据开销
// 入参: pixels 目标纹理, source 通道图像
// 返回: error 格式、预算或取消错误
func (r *u3dRender) textureChannels(pixels *image.NRGBA64, source U3DTextureImage) error {
	var config func(io.Reader) (image.Config, error)
	var decode func(io.Reader) (image.Image, error)
	switch source.Compression {
	case 1, 3:
		config, decode = jpeg.DecodeConfig, jpeg.Decode
	case 2:
		config, decode = png.DecodeConfig, png.Decode
	case 4:
		config, decode = tiff.DecodeConfig, tiff.Decode
	default:
		return fmt.Errorf("invalid image compression")
	}
	budget := r.remaining
	defer func() { r.remaining = budget }()
	if err := r.reserve(uint64(len(source.Data)), 4); err != nil {
		return err
	}
	if err := r.reserve(1, 1<<20); err != nil {
		return err
	}
	reader := &u3dImageReader{ctx: r.ctx, data: bytes.NewReader(source.Data)}
	cfg, err := config(reader)
	if err != nil {
		return err
	}
	if cfg.Width != pixels.Rect.Dx() || cfg.Height != pixels.Rect.Dy() {
		return fmt.Errorf("image dimensions differ from texture declaration")
	}
	if source.Compression == 3 && cfg.ColorModel != color.GrayModel || source.Compression == 1 && cfg.ColorModel != color.YCbCrModel {
		return fmt.Errorf("invalid JPEG texture color model")
	}
	w, h := uint64(cfg.Width)+16, uint64(cfg.Height)+16
	if source.Compression == 4 {
		w, h = max(w, 1024), max(h, 1024)
	}
	if err := r.reserve(w*h, 32); err != nil {
		return err
	}
	reader.data.Reset(source.Data)
	img, err := decode(reader)
	if err != nil {
		return err
	}
	if img.Bounds() != pixels.Bounds() {
		return fmt.Errorf("invalid decoded image dimensions")
	}
	channels := u3dTextureChannels(source.Channels)
	grayAlpha := source.Channels == 1 && (cfg.ColorModel == color.GrayModel || cfg.ColorModel == color.Gray16Model)
	for y := range cfg.Height {
		for x := range cfg.Width {
			if x%4096 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			value := baseimage.NRGBA64At(img, x, y)
			current := pixels.NRGBA64At(x, y)
			if channels&8 != 0 {
				current.R = value.R
			}
			if channels&4 != 0 {
				current.G = value.G
			}
			if channels&2 != 0 {
				current.B = value.B
			}
			if channels&1 != 0 {
				current.A = value.A
				if grayAlpha {
					current.A = value.R
				}
			}
			pixels.SetNRGBA64(x, y, current)
		}
	}
	return r.ctx.Err()
}
