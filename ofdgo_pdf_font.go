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
	"context"
	"crypto/sha256"
	"fmt"
	"unicode/utf8"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfImportedFont 保存单次导入共用的字体资源、度量及源字形映射，不保存文档标识
type pdfImportedFont struct {
	resource    *FontResource
	checksum    [32]byte
	metrics     FontMetrics
	type1Glyphs map[string]uint16
	repairLimit uint16
}

// shapePDFText 使用导入字体整形注释文字，将毫米坐标约定转换为PDF文字空间方向
// 入参: ctx 取消上下文, source 源字体, text 单行原文, rightToLeft 段落方向
// 返回: []pdfgo.ShapedGlyph 千分之一字号的定位字形, error 字体、整形或取消错误
func (p *pdfImporter) shapePDFText(ctx context.Context, source *pdfgo.Font, text string, rightToLeft bool) ([]pdfgo.ShapedGlyph, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(source.Program) == 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation shaping without embedded font"}
	}
	font, err := p.importedFont(source)
	if err != nil {
		return nil, err
	}
	shaper, ok := font.metrics.(FontShaperOptions)
	if !ok {
		return nil, &pdfgo.UnsupportedError{Feature: "annotation text shaping backend"}
	}
	options := TextShapeOptions{Bidi: true}
	if rightToLeft {
		options.Direction = "rtl"
	}
	glyphs, err := shaper.ShapeTextWithOptions(text, 1000, options)
	if err != nil {
		return nil, err
	}
	result := make([]pdfgo.ShapedGlyph, len(glyphs))
	units := float64(font.metrics.UnitsPerEm())
	if units <= 0 {
		return nil, fmt.Errorf("invalid annotation font units")
	}
	for i, glyph := range glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i] = pdfgo.ShapedGlyph{ID: glyph.Glyph, Cluster: glyph.Cluster, VisualOrder: glyph.VisualOrder, X: glyph.X, Y: -glyph.Y, Advance: glyph.Advance, Width: float64(font.metrics.GlyphAdvance(glyph.Glyph)) * 1000 / units}
	}
	return result, ctx.Err()
}

// embeddedTextFont 匹配同名内嵌字体，仅复用唯一且覆盖全部字符的字形程序
// 入参: source 外部字体, glyphs 当前文字字形
// 返回: *pdfImportedFont 可复用字体，无可靠匹配时为空
func (p *pdfImporter) embeddedTextFont(source *pdfgo.Font, glyphs []pdfgo.Glyph) *pdfImportedFont {
	name := codecpdf.EmbeddedFontName(source.Name)
	if name == "" || len(glyphs) == 0 {
		return nil
	}
	var matched *pdfImportedFont
	for _, imported := range p.fonts {
		if codecpdf.EmbeddedFontName(imported.resource.Font.FontName) != name {
			continue
		}
		if matched != nil && matched.checksum != imported.checksum {
			return nil
		}
		matched = imported
	}
	if matched == nil {
		return nil
	}
	for _, glyph := range glyphs {
		if glyph.Name == ".notdef" || utf8.RuneCountInString(glyph.Text) != 1 {
			return nil
		}
		char, _ := utf8.DecodeRuneInString(glyph.Text)
		id := matched.metrics.GlyphIndex(char)
		if id == 0 || id >= matched.metrics.NumGlyphs() || matched.repairLimit != 0 && id >= matched.repairLimit {
			return nil
		}
	}
	return matched
}

// importedFont 按原PDF字体复用包装和解析结果，失败结果不进入缓存
// 入参: source 源字体
// 返回: *pdfImportedFont 只读导入字体, error 字体程序或后端错误
func (p *pdfImporter) importedFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	if cached := p.fonts[source]; cached != nil {
		return cached, nil
	}
	var type1 *basefont.Type1Program
	result := &pdfImportedFont{}
	if source.ProgramType == "FontFile" {
		parsed, err := basefont.ParseType1Program(source.Program)
		if err != nil {
			return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
		}
		type1 = &parsed
		result.type1Glyphs = parsed.GlyphIDs()
	}
	program, repair, err := codecpdf.FontProgram(source, type1)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, err)
	}
	if repair.Locations && p.warning == nil {
		return nil, fmt.Errorf("PDF font %s program: %w", source.Name, basefont.ErrTTInvalidLocations)
	}
	if p.editor.backends.FontResources == nil {
		return nil, fmt.Errorf("PDF font %s resource: font resources: %w", source.Name, ErrBackendUnavailable)
	}
	result.resource, err = p.editor.backends.FontResources.OpenFontResource(FontFile{Name: source.Name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s resource: %w", source.Name, err)
	}
	if p.editor.backends.Fonts == nil {
		return nil, fmt.Errorf("PDF font backend unavailable")
	}
	result.metrics, err = p.editor.backends.Fonts.OpenFont(result.resource.Data)
	if err != nil {
		return nil, fmt.Errorf("PDF font %s metrics: %w", source.Name, err)
	}
	if _, ok := result.metrics.(FontOutlines); !ok {
		return nil, fmt.Errorf("PDF font backend does not provide glyph outlines")
	}
	result.checksum, result.repairLimit = sha256.Sum256(result.resource.Data), repair.MetricsLimit
	if repair.Locations {
		p.warning(pdfgo.Diagnostic{Message: fmt.Sprintf("PDF font %s has invalid glyph locations; index rebuilt with original glyph IDs", source.Name)})
	}
	if p.fonts == nil {
		p.fonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	p.fonts[source] = result
	return result, nil
}

// symbolFont 加载标准符号字体，保留原字形编号和源字形名称
// 入参: source 未嵌入的PDF字体
// 返回: *pdfImportedFont 可用的符号字体，无匹配时为空, error 字体或取消错误
func (p *pdfImporter) symbolFont(source *pdfgo.Font) (*pdfImportedFont, error) {
	name := codecpdf.EmbeddedFontName(source.Name)
	if source.Subtype != "Type1" || name != "Symbol" && name != "ZapfDingbats" {
		return nil, nil
	}
	if imported, ok := p.symbolFonts[source]; ok {
		return imported, nil
	}
	if p.symbolFonts == nil {
		p.symbolFonts = make(map[*pdfgo.Font]*pdfImportedFont)
	}
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	definition := &Font{ID: name, FontName: name, FamilyName: name}
	reader := &Reader{doc: &Document{}, fontResourcesRead: true, fontCache: map[string]*Font{name: definition}}
	resolved, err := p.editor.newRenderer(reader).ResolveFont(name, true)
	if err != nil || len(resolved.Data) == 0 || p.editor.backends.FontResources == nil {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	program, names, err := basefont.SymbolProgram(p.ctx, resolved.Data, name)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	resource, err := p.editor.backends.FontResources.OpenFontResource(FontFile{Name: name + ".ttf", Data: program}, 0)
	if err != nil {
		return nil, err
	}
	metrics, err := p.editor.backends.Fonts.OpenFont(resource.Data)
	if err != nil {
		return nil, err
	}
	if _, ok := metrics.(FontOutlines); !ok {
		p.symbolFonts[source] = nil
		return nil, nil
	}
	result := &pdfImportedFont{resource: resource, checksum: sha256.Sum256(resource.Data), metrics: metrics, type1Glyphs: names}
	p.symbolFonts[source] = result
	return result, nil
}
