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
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfDestinationFontBackend 禁止内容度量忽略实际绘制所需的缺失字体
type pdfDestinationFontBackend struct {
	FontBackend
}

// ResolveFont 沿用字体匹配，只在实际绘制缺少字体数据时中止度量
// 入参: renderer 度量渲染器, id 字体标识, exact 是否禁止无关回退
// 返回: ResolvedFont 字体资源, error 解析或缺失错误
func (b pdfDestinationFontBackend) ResolveFont(renderer *Renderer, id string, exact bool) (ResolvedFont, error) {
	font, err := b.FontBackend.ResolveFont(renderer, id, exact)
	if err == nil && len(font.Data) == 0 {
		err = fmt.Errorf("PDF destination font %q unavailable", id)
	}
	return font, err
}

// destinationBounds 度量目标页内容并缓存页面内范围，不包含注解或像素取整
// 入参: page PDF目标页
// 返回: Box OFD内容范围, error 度量错误
func (p *pdfImporter) destinationBounds(page *pdfgo.Page) (Box, error) {
	if err := p.ctx.Err(); err != nil {
		return Box{}, err
	}
	if box, ok := p.destinationBoxes[page.Reference]; ok {
		return box, nil
	}
	if index, ok := p.pageIndexes[page.Reference]; ok {
		previous := p.page
		p.page = index
		defer func() { p.page = previous }()
	}
	matrix, width, height := codecpdf.PageMatrix(page)
	editor := NewEditor()
	editor.SetRenderBackends(p.editor.Backends())
	editor.SetFontDirs(p.editor.fontDirs...)
	editor.SetFontFS(p.editor.fontFS...)
	editor.fontSourcesCache = p.renderer.fontSourcesCache
	editor.validatedPaths = p.editor.pathValidationCache()
	if _, err := editor.AddPage(width, height); err != nil {
		return Box{}, err
	}
	local := pdfImporter{
		ctx: p.ctx, reader: p.reader, editor: editor, renderer: p.renderer,
		matrix: matrix, pageBox: page.CropBox, pageWidth: width, pageHeight: height,
		rasterDPI: p.rasterDPI, fontIDs: make(map[*pdfgo.Font]string),
		resolveFile: p.resolveFile, resolveReference: p.resolveReference,
		references: &codecpdf.ReferenceCache{},
		halftones:  p.halftones, halftoneWarnings: make(map[*pdfgo.Halftone]bool),
	}
	defer local.references.Close()
	if err := local.compositePage(nil, func(visitor pdfgo.Visitor) error {
		return p.reader.WalkPage(p.ctx, page, visitor)
	}); err != nil {
		return Box{}, fmt.Errorf("PDF destination content: %w", err)
	}
	if err := local.commitObjects(); err != nil {
		return Box{}, err
	}
	reader, err := editor.resourceReader()
	if err != nil {
		return Box{}, err
	}
	defer reader.Close()
	renderer := p.renderer.childRenderer(reader)
	renderer.TransparentBackground = true
	renderer.RenderAnnotations = false
	if renderer.backends.Fonts != nil {
		renderer.backends.Fonts = pdfDestinationFontBackend{renderer.backends.Fonts}
	}
	scene, err := renderer.CompilePageContext(p.ctx, &editor.pages[0])
	if err != nil {
		return Box{}, err
	}
	if scene == nil {
		return Box{}, fmt.Errorf("PDF destination content scene unavailable")
	}
	geometry, err := renderer.Geometry()
	if err != nil {
		return Box{}, err
	}
	box, err := scene.BoundsContext(p.ctx, geometry)
	if err != nil {
		return Box{}, err
	}
	left, top := math.Max(0, box.X), math.Max(0, box.Y)
	right, bottom := math.Min(width, box.X+box.W), math.Min(height, box.Y+box.H)
	box = Box{X: left, Y: top, W: right - left, H: bottom - top}
	if !finite(left) || !finite(top) || !finite(right) || !finite(bottom) || box.W <= 0 || box.H <= 0 {
		return Box{}, fmt.Errorf("PDF destination content bounds empty or invalid")
	}
	if err := p.ctx.Err(); err != nil {
		return Box{}, err
	}
	if p.destinationBoxes == nil {
		p.destinationBoxes = make(map[pdfgo.Reference]Box)
	}
	p.destinationBoxes[page.Reference] = box
	return box, nil
}

// outlines 按先序批量转换目录，保留层级、展开状态和动作顺序
// 入参: ctx 取消上下文
// 返回: error 严格模式转换错误或取消错误
func (p *pdfImporter) outlines(ctx context.Context) error {
	count := 0
	data, err := encodeOFDXMLContext(ctx, func(x *ofdXML) {
		x.root("Outlines", nil)
		depth := 0
		err := p.reader.WalkOutlines(ctx, func(level int, item pdfgo.OutlineItem) error {
			actions, err := p.outlineActions(ctx, item)
			if err != nil {
				if p.warning == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errPDFMediaRead) {
					return err
				}
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outline %q actions not transferred to OFD: %v", item.Title, err)})
				actions = nil
			}
			for depth > level {
				x.end("OutlineElem")
				depth--
			}
			attrs := ofdAttrs{{Name: xml.Name{Local: "Title"}, Value: item.Title}}
			attrs.add("Expanded", strconv.FormatBool(item.Expanded))
			x.start("OutlineElem", attrs)
			x.actions(actions)
			depth++
			count++
			return x.err
		})
		if err != nil {
			x.err = err
			return
		}
		for depth > 0 {
			x.end("OutlineElem")
			depth--
		}
		x.end("Outlines")
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.warning == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errPDFMediaRead) {
			return err
		}
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outlines not transferred to OFD: %v", err)})
		return nil
	}
	if count > 0 {
		p.editor.outlines = bytes.TrimPrefix(data, []byte(xml.Header))
	}
	return nil
}

// outlineActions 转换目录目标及动作链，不为无目标节点补充跳转
// 入参: ctx 取消上下文, item PDF目录节点
// 返回: []Action OFD动作序列, error 动作或取消错误
func (p *pdfImporter) outlineActions(ctx context.Context, item pdfgo.OutlineItem) ([]Action, error) {
	object, err := p.reader.Resolve(item.Action)
	if err != nil {
		return nil, err
	}
	dest, err := p.reader.Resolve(item.Destination)
	if err != nil {
		return nil, err
	}
	if dest != nil {
		if object != nil {
			return nil, fmt.Errorf("PDF outline has both destination and action")
		}
		object = pdfgo.Dictionary{"S": pdfgo.Name("GoTo"), "D": dest}
	}
	currentPage := -1
	var actions []Action
	err = p.reader.WalkActions(ctx, object, func(info pdfgo.ActionInfo) error {
		converted, err := p.linkActions(pdfgo.Annotation{Dictionary: pdfgo.Dictionary{"A": info.Dictionary}}, p.warning == nil, &currentPage)
		if err != nil {
			if errors.Is(err, errPDFMediaRead) {
				return err
			}
			var unsupported *pdfgo.UnsupportedError
			if p.warning != nil && (errors.As(err, &unsupported) || errors.Is(err, pdfgo.ErrInvalidDestination) || errors.Is(err, pdfgo.ErrInvalidAction)) {
				p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF outline action not transferred to OFD: %v", err)})
				currentPage = -1
				return nil
			}
			return err
		}
		if converted != nil {
			actions = append(actions, converted...)
		} else {
			currentPage = -1
		}
		return nil
	})
	return actions, err
}
