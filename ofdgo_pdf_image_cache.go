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
	"image/color"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfImageContentKey 区分编码内容、渲染意图、原生输出空间、模板填充色及有损采样尺寸
type pdfImageContentKey struct {
	data    [32]byte
	intent  pdfgo.Name
	process pdfgo.Name
	tint    color.NRGBA64
	size    image.Point
}

// pdfImageContent 保存不依赖间接资源的图像描述及已注册编号
type pdfImageContent struct {
	source *pdfgo.Image
	id     string
}

// imageContentKey 对无间接依赖的小图像生成内容键，超大图像仍沿用流缓存
// 入参: source 图像描述, key 流缓存键
// 返回: pdfImageContentKey 内容键, bool 是否可复用, error 取消错误
func (p *pdfImporter) imageContentKey(source *pdfgo.Image, key pdfImageKey) (pdfImageContentKey, bool, error) {
	digest, reusable, err := codecpdf.ImageDigest(p.ctx, source)
	if !reusable {
		return pdfImageContentKey{}, reusable, err
	}
	return pdfImageContentKey{data: digest, intent: key.intent, process: key.process, tint: key.tint, size: key.size}, true, err
}

// matches 核对完整编码与有效图像属性，不以摘要相同代替内容相同
// 入参: source 待复用图像
// 返回: bool 是否可使用同一图像资源
func (c pdfImageContent) matches(source *pdfgo.Image) bool {
	return codecpdf.SameImage(c.source, source)
}
