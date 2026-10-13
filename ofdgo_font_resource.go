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
	"encoding/binary"
	"fmt"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// FontResource 保存独立字体数据、标准元信息与只读度量，不包含绘图库类型
type FontResource struct {
	FontMetrics
	Data      []byte
	Font      Font
	Extension string
	CanSubset bool
}

// FontUsage 描述全包已确认的字体引用，Unsafe禁止裁剪
type FontUsage struct {
	Characters []rune
	Glyphs     []uint16
	Unsafe     bool
}

// FontResourceBackend 解析和裁剪保存资源，独立于显示字体后端
// SubsetFont的preserve为true时不得改变字形编号，SubsetSourceFont无安全收益时返回nil
type FontResourceBackend interface {
	Backend
	OpenFontResource(file FontFile, index int) (*FontResource, error)
	SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error)
	SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error)
}

// SFNTBackend 提供独立的OpenType字体度量、轮廓和资源处理
type SFNTBackend struct{}

// sfntResourceMetrics 隔离资源解析器的度量实现
type sfntResourceMetrics struct{ *basefont.SFNT }

// Name 返回资源后端标识
// 返回: string 后端标识
func (SFNTBackend) Name() string { return "sfnt" }

// OpenFontResource 提取独立字体并读取名称、样式和裁剪能力
// CFF2使用默认实例的静态轮廓，不改变原字体文件
// 入参: file 字体文件, index 集合索引
// 返回: *FontResource 字体资源, error 解析错误
func (SFNTBackend) OpenFontResource(file FontFile, index int) (*FontResource, error) {
	data, err := file.Face(index)
	if err != nil {
		return nil, err
	}
	data, err = basefont.DefaultCFF2Font(data)
	if err != nil {
		return nil, err
	}
	sfnt, err := basefont.ParseSFNT(data)
	if err != nil {
		return nil, err
	}
	name := basefont.SFNTName(sfnt, 6, 4, 1)
	if name == "" {
		return nil, fmt.Errorf("font has no name")
	}
	extension := ".ttf"
	if sfnt.IsCFF {
		extension = ".otf"
	}
	return &FontResource{
		FontMetrics: sfntResourceMetrics{sfnt}, Data: data, Extension: extension,
		Font:      Font{FontName: name, FamilyName: basefont.SFNTName(sfnt, 16, 1), Charset: "unicode", Bold: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&1 != 0, Italic: binary.BigEndian.Uint16(sfnt.Tables["head"][44:])&2 != 0, FixedWidth: len(sfnt.Tables["post"]) >= 16 && binary.BigEndian.Uint32(sfnt.Tables["post"][12:]) != 0},
		CanSubset: basefont.Subsettable(sfnt),
	}, nil
}

// SubsetFont 裁剪新建字体并按要求保留显式字形编号
// 入参: data 字体数据, glyphs 用字, preserve 是否保留编号
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetFont(data []byte, glyphs []uint16, preserve bool) ([]byte, error) {
	return basefont.SubsetSFNT(data, glyphs, preserve)
}

// SubsetSourceFont 按全包引用裁剪原字体，不确定时保留原资源
// 入参: data 字体数据, usage 用字信息
// 返回: []byte 子集, error 裁剪错误
func (SFNTBackend) SubsetSourceFont(data []byte, usage FontUsage) ([]byte, error) {
	u := &basefont.Usage{Characters: make(map[rune]bool), Glyphs: make(map[uint16]bool), Unsafe: usage.Unsafe}
	for _, char := range usage.Characters {
		u.Characters[char] = true
	}
	for _, glyph := range usage.Glyphs {
		u.Glyphs[glyph] = true
	}
	return basefont.SubsetSourceSFNT(data, u), nil
}

// Write 返回未改动元信息的字体数据
// 返回: []byte 字体数据
func (f sfntResourceMetrics) Write() []byte { return f.SFNT.Write() }
