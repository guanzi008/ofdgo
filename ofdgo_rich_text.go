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
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/go-text/typesetting/segmenter"
)

// ErrNotRichText 表示对象没有富文本信息且不属于可提升的普通文字。
var ErrNotRichText = errors.New("object is not rich text")

// RichText 保存有序文字片段及段落选项，长度和范围使用Unicode码点而非字节。
type RichText struct {
	Runs   []RichTextRun `json:"runs"`
	Layout TextLayout    `json:"layout"`
}

// RichTextRun 保存文字及完整格式，换行使用LF，长度单位为毫米。
type RichTextRun struct {
	Text   string     `json:"text"`
	Format TextFormat `json:"format"`
}

// TextFormat 保存完整文字格式，Font为当前文档字体资源标识，颜色使用OFD RGB分量。
type TextFormat struct {
	Font       string  `json:"font"`
	Size       float64 `json:"size"`
	Color      string  `json:"color"`
	Bold       bool    `json:"bold"`
	Italic     bool    `json:"italic"`
	Underline  bool    `json:"underline"`
	Strikeout  bool    `json:"strikeout"`
	Shadow     bool    `json:"shadow"`
	Background string  `json:"background"`
}

// TextFormatPatch 保存格式增量，nil保持原值，false和空背景显式清除对应格式。
type TextFormatPatch struct {
	Font       *string  `json:"font,omitempty"`
	Size       *float64 `json:"size,omitempty"`
	Color      *string  `json:"color,omitempty"`
	Bold       *bool    `json:"bold,omitempty"`
	Italic     *bool    `json:"italic,omitempty"`
	Underline  *bool    `json:"underline,omitempty"`
	Strikeout  *bool    `json:"strikeout,omitempty"`
	Shadow     *bool    `json:"shadow,omitempty"`
	Background *string  `json:"background,omitempty"`
}

// Text 返回包含显式换行的原文，不包含排版产生的软换行。
// 返回: string 完整原文
func (v RichText) Text() string {
	var text strings.Builder
	for _, run := range v.Runs {
		text.WriteString(run.Text)
	}
	return text.String()
}

// Len 返回原文的Unicode码点数量。
// 返回: int 码点数量
func (v RichText) Len() int { return utf8.RuneCountInString(v.Text()) }

// Validate 校验文字、格式、段落选项及跨片段的字素边界，不加载字体。
// 返回: error 无效文字、格式或边界
func (v RichText) Validate() error {
	if len(v.Runs) == 0 {
		return fmt.Errorf("rich text requires at least one run")
	}
	if err := validateRichLayout(v.Layout); err != nil {
		return err
	}
	bounds := richTextBoundaries(v.Text())
	position := 0
	for _, run := range v.Runs {
		if !utf8.ValidString(run.Text) || !structureXMLText(run.Text) || strings.ContainsAny(run.Text, "\r\t") {
			return fmt.Errorf("rich text requires XML-compatible UTF-8 and LF line breaks without tabs")
		}
		if !bounds[position] {
			return fmt.Errorf("rich text format boundary splits a grapheme")
		}
		position += utf8.RuneCountInString(run.Text)
		if err := run.Format.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validate 校验完整文字格式，不解析文档资源。
// 返回: error 格式错误
func (f TextFormat) validate() error {
	if strings.TrimSpace(f.Font) == "" || !finite(f.Size) || f.Size <= 0 {
		return fmt.Errorf("rich text requires a font and positive finite size")
	}
	for _, color := range []string{f.Color, f.Background} {
		if color != "" {
			values, err := creationNumbers(color, 3)
			if err != nil {
				return fmt.Errorf("rich text color requires three RGB components: %w", err)
			}
			for _, value := range values {
				if value < 0 || value > 255 {
					return fmt.Errorf("rich text RGB components must be between 0 and 255")
				}
			}
		}
	}
	return nil
}

// validateRichLayout 校验富文本段落的有限数值及枚举。
// 入参: v 段落选项
// 返回: error 无效选项
func validateRichLayout(v TextLayout) error {
	if !finite(v.LineHeight) || v.LineHeight < 0 || !finite(v.LetterSpacing) || !finite(v.LeftIndent) || !finite(v.RightIndent) || !finite(v.FirstLineIndent) {
		return fmt.Errorf("invalid rich text layout dimensions")
	}
	switch v.Align {
	case "", "left", "center", "right", "justify":
	default:
		return fmt.Errorf("invalid rich text alignment")
	}
	return nil
}

// richTextBoundaries 返回包含首尾位置的扩展字素边界。
// 入参: value 原文
// 返回: map[int]bool 有效码点边界
func richTextBoundaries(value string) map[int]bool {
	runes := []rune(value)
	result := map[int]bool{0: true, len(runes): true}
	var seg segmenter.Segmenter
	seg.Init(runes)
	for it := seg.GraphemeIterator(); it.Next(); {
		g := it.Grapheme()
		result[g.Offset] = true
	}
	return result
}

// checkRange 检查半开范围及其字素边界。
// 入参: start 起点, end 终点
// 返回: error 无效范围
func (v RichText) checkRange(start, end int) error {
	if err := v.Validate(); err != nil {
		return err
	}
	bounds := richTextBoundaries(v.Text())
	if start < 0 || end < start || end > v.Len() || !bounds[start] || !bounds[end] {
		return fmt.Errorf("rich text range must end at grapheme boundaries")
	}
	return nil
}

// Slice 返回半开范围的独立片段，空范围保留插入位置的格式。
// 入参: start 起点, end 终点
// 返回: RichText 独立片段, error 无效范围
func (v RichText) Slice(start, end int) (RichText, error) {
	if err := v.checkRange(start, end); err != nil {
		return RichText{}, err
	}
	result := RichText{Layout: v.Layout, Runs: v.sliceRuns(start, end)}
	if len(result.Runs) == 0 {
		position := 0
		format := v.Runs[len(v.Runs)-1].Format
		for _, run := range v.Runs {
			position += utf8.RuneCountInString(run.Text)
			if start < position {
				format = run.Format
				break
			}
		}
		result.Runs = []RichTextRun{{Format: format}}
	}
	return result, nil
}

// sliceRuns 提取已校验范围，不保留空片段。
// 入参: start 起点, end 终点
// 返回: []RichTextRun 独立片段
func (v RichText) sliceRuns(start, end int) []RichTextRun {
	var result []RichTextRun
	position := 0
	for _, run := range v.Runs {
		chars := []rune(run.Text)
		a, b := max(0, start-position), min(len(chars), end-position)
		if a < b {
			result = append(result, RichTextRun{Text: string(chars[a:b]), Format: run.Format})
		}
		position += len(chars)
	}
	return result
}

// Replace 原子替换半开范围，空片段列表表示删除，相邻相同格式自动合并。
// 入参: start 起点, end 终点, runs 替换片段
// 返回: error 范围或内容错误
func (v *RichText) Replace(start, end int, runs []RichTextRun) error {
	if v == nil {
		return fmt.Errorf("nil rich text")
	}
	if err := v.checkRange(start, end); err != nil {
		return err
	}
	next := RichText{Layout: v.Layout, Runs: v.sliceRuns(0, start)}
	next.Runs = append(next.Runs, runs...)
	next.Runs = append(next.Runs, v.sliceRuns(end, v.Len())...)
	if len(next.Runs) == 0 {
		caret, _ := v.Slice(start, start)
		next.Runs = caret.Runs
	}
	next = normalizeRichText(next)
	if err := next.Validate(); err != nil {
		return err
	}
	*v = next
	return nil
}

// ApplyFormat 原子修改半开范围的格式，空范围不改变正文。
// 入参: start 起点, end 终点, patch 格式增量
// 返回: error 范围或格式错误
func (v *RichText) ApplyFormat(start, end int, patch TextFormatPatch) error {
	if v == nil {
		return fmt.Errorf("nil rich text")
	}
	part, err := v.Slice(start, end)
	if err != nil {
		return err
	}
	for i := range part.Runs {
		part.Runs[i].Format = patch.apply(part.Runs[i].Format)
	}
	if err := part.Validate(); err != nil {
		return err
	}
	if start == end {
		return nil
	}
	return v.Replace(start, end, part.Runs)
}

// apply 应用显式给出的格式字段。
// 入参: f 完整原格式
// 返回: TextFormat 修改后的格式
func (p TextFormatPatch) apply(f TextFormat) TextFormat {
	if p.Font != nil {
		f.Font = *p.Font
	}
	if p.Size != nil {
		f.Size = *p.Size
	}
	if p.Color != nil {
		f.Color = *p.Color
	}
	if p.Bold != nil {
		f.Bold = *p.Bold
	}
	if p.Italic != nil {
		f.Italic = *p.Italic
	}
	if p.Underline != nil {
		f.Underline = *p.Underline
	}
	if p.Strikeout != nil {
		f.Strikeout = *p.Strikeout
	}
	if p.Shadow != nil {
		f.Shadow = *p.Shadow
	}
	if p.Background != nil {
		f.Background = *p.Background
	}
	return f
}

// normalizeRichText 合并相同格式，保留空文档的插入格式。
// 入参: v 完整富文本
// 返回: RichText 归一化副本
func normalizeRichText(v RichText) RichText {
	runs := make([]RichTextRun, 0, len(v.Runs))
	for _, run := range v.Runs {
		if len(runs) > 0 && runs[len(runs)-1].Format == run.Format {
			runs[len(runs)-1].Text += run.Text
		} else {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 && len(v.Runs) > 0 {
		runs = append(runs, RichTextRun{Format: v.Runs[0].Format})
	}
	v.Runs = runs
	return v
}
