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

package threed

import (
	"fmt"
)

// U3DTexture 保存纹理尺寸、图像类型及各通道的图像数据
type U3DTexture struct {
	Name          string
	Width, Height uint32
	ImageType     uint8
	Images        []U3DTextureImage
}

// U3DTextureImage 保存压缩格式、通道位图及完整压缩数据，外部图像仅保留候选URL
// Compression依次以1至4表示JPEG-24、PNG、JPEG-8和TIFF，ByteCount仅用于内置图像
type U3DTextureImage struct {
	Compression uint8
	Channels    uint8
	External    bool
	ByteCount   uint32
	Data        []byte
	URLs        []string
}

// u3dTextureChannels 将亮度通道展开为RGB覆盖位，保留透明度位
// 入参: channels 标准通道位图
// 返回: uint8 实际覆盖通道
func u3dTextureChannels(channels uint8) uint8 {
	if channels&16 != 0 {
		channels = channels&^16 | 14
	}
	return channels
}

// textureDeclaration 按ECMA-363第9.8.5.1节读取纹理及通道图像声明
// 入参: r 字段读取器, name 纹理名称
// 返回: error 格式、预算或取消错误
func (d *u3dDecoder) textureDeclaration(r *u3dValues, name string) error {
	if _, exists := d.textures[name]; exists {
		return fmt.Errorf("duplicate U3D texture %q", name)
	}
	texture := U3DTexture{Name: name, Height: r.u32(), Width: r.u32(), ImageType: r.u8()}
	if texture.Width == 0 || texture.Height == 0 {
		return fmt.Errorf("invalid U3D texture dimensions")
	}
	switch texture.ImageType {
	case 1, 14, 15, 16, 17:
	default:
		return fmt.Errorf("invalid U3D texture image type")
	}
	count := r.u32()
	if r.err != nil {
		return r.err
	}
	if count == 0 || count > 4 || uint64(count) > uint64(len(r.data)-r.pos)/8 {
		return fmt.Errorf("invalid U3D continuation image count")
	}
	if err := d.reserve(uint64(count), 64); err != nil {
		return err
	}
	texture.Images = make([]U3DTextureImage, int(count))
	var covered uint8
	for n := range texture.Images {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		image := &texture.Images[n]
		image.Compression, image.Channels = r.u8(), r.u8()
		attributes := uint16(r.u8()) | uint16(r.u8())<<8
		if r.err != nil {
			return r.err
		}
		channels := u3dTextureChannels(image.Channels)
		if image.Compression < 1 || image.Compression > 4 || image.Channels == 0 || image.Channels&^uint8(31) != 0 || attributes > 1 || covered&channels != 0 || channels&^u3dTextureChannels(texture.ImageType) != 0 {
			return fmt.Errorf("invalid U3D continuation image format")
		}
		covered |= channels
		image.External = attributes == 1
		if !image.External {
			image.ByteCount = r.u32()
			continue
		}
		count := r.u32()
		if r.err != nil {
			return r.err
		}
		if uint64(count) > uint64(len(r.data)-r.pos)/2 {
			return fmt.Errorf("invalid U3D texture URL count")
		}
		if err := d.reserve(uint64(count), 16); err != nil {
			return err
		}
		image.URLs = make([]string, int(count))
		for i := range image.URLs {
			if err := d.ctx.Err(); err != nil {
				return err
			}
			image.URLs[i] = r.text()
			if r.err != nil {
				return r.err
			}
		}
	}
	if err := r.done(); err != nil {
		return err
	}
	if covered != u3dTextureChannels(texture.ImageType) {
		return fmt.Errorf("incomplete U3D texture channels")
	}
	if d.textures == nil {
		d.textures = make(map[string]int)
	}
	d.textures[name] = len(d.model.Textures)
	d.model.Textures = append(d.model.Textures, texture)
	return nil
}

// textureContinuation 按声明长度合并纹理分片，不与输入共享存储
// 入参: r 字段读取器, name 纹理名称
// 返回: error 格式、预算或取消错误
func (d *u3dDecoder) textureContinuation(r *u3dValues, name string) error {
	index := r.u32()
	if r.err != nil {
		return r.err
	}
	texture, exists := d.textures[name]
	if !exists || uint64(index) >= uint64(len(d.model.Textures[texture].Images)) {
		return fmt.Errorf("invalid U3D texture continuation reference")
	}
	image := &d.model.Textures[texture].Images[index]
	data := r.take(len(r.data) - r.pos)
	if image.External || uint64(len(image.Data))+uint64(len(data)) > uint64(image.ByteCount) {
		return fmt.Errorf("invalid U3D texture continuation length")
	}
	if image.Data == nil {
		if err := d.reserve(uint64(image.ByteCount), 1); err != nil {
			return err
		}
		image.Data = make([]byte, 0, int(image.ByteCount))
	}
	for len(data) != 0 {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		count := min(65536, len(data))
		image.Data = append(image.Data, data[:count]...)
		data = data[count:]
	}
	return d.ctx.Err()
}

// checkTextures 确认各通道的内置图像完整，不加载外部引用
// 返回: error 缺失分片或取消错误
func (d *u3dDecoder) checkTextures() error {
	for _, texture := range d.model.Textures {
		for _, image := range texture.Images {
			if err := d.ctx.Err(); err != nil {
				return err
			}
			if !image.External && uint64(len(image.Data)) != uint64(image.ByteCount) {
				return fmt.Errorf("incomplete U3D texture %q", texture.Name)
			}
		}
	}
	return nil
}
