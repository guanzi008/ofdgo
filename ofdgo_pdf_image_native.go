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
	"fmt"
	"image"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// imageProcessModel 选择与页面原生过程色一致的图像输出，已等价的备用色沿用现有编码路径
// 入参: source 源图像
// 返回: pdfgo.Name 原生输出空间，无需重映射时为空, error 色料解析错误
func (p *pdfImporter) imageProcessModel(source *pdfgo.Image) (pdfgo.Name, error) {
	space := p.compositeSpace
	if source.ImageMask || !space.Device() {
		return "", nil
	}
	colorants, err := source.ColorantSpace()
	if err != nil {
		return "", err
	}
	process := colorants.PrepareProcess(&pdfgo.ColorantDevice{Space: space}, space, false)
	if process == nil || process.Mask() == ([4]bool{}) || process.AlternateEquivalent() {
		return "", nil
	}
	return space.Model, nil
}

// imageProcess 复用源分量并保留原始分辨率，颜色变换随编码逐像素执行
// 入参: source 源图像, model 原生设备空间
// 返回: image.Image 只读图像, error 解码或分量映射错误
func (p *pdfImporter) imageProcess(source *pdfgo.Image, model pdfgo.Name) (image.Image, error) {
	cache := p.compositingCache()
	space := &pdfgo.ColorSpace{Model: model}
	process, err := cache.imageProcess(p, source, space, false)
	if err != nil {
		return nil, err
	}
	if process == nil {
		return nil, fmt.Errorf("invalid native PDF image process space %s", model)
	}
	return &codecpdf.NativeImage{Source: process, Space: space}, nil
}

// imageProcess 复用同组原生采样，仅在需要备用颜色时保留完整分量
// 入参: importer 导入器, source 源图像, space 组混合空间, softMask 是否用于软蒙版
// 返回: *pdfgo.ImageProcess 原生采样，需要备用色时为nil, error 解码或布局错误
func (c *pdfCompositeCache) imageProcess(importer *pdfImporter, source *pdfgo.Image, space *pdfgo.ColorSpace, softMask bool) (*pdfgo.ImageProcess, error) {
	key := pdfImageProcessKey{image: source, space: space, softMask: softMask}
	if process, found := c.imageProcesses[key]; found {
		return process, nil
	}
	device := &pdfgo.ColorantDevice{Space: space}
	var process *pdfgo.ImageProcess
	var err error
	if components := c.images[source]; components != nil {
		process, err = components.PrepareProcess(device, space, softMask)
	} else {
		process, err = source.DecodeProcessContext(importer.ctx, device, space, softMask)
	}
	if err != nil {
		return nil, err
	}
	if c.imageProcesses == nil {
		c.imageProcesses = make(map[pdfImageProcessKey]*pdfgo.ImageProcess)
	}
	c.imageProcesses[key] = process
	return process, nil
}
