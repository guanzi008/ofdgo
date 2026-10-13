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
	"unicode/utf8"

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// FontFace 字体文件中的字体名称与零起始索引，不依赖文件名
type FontFace = basefont.Face

// Faces 读取字体名称表，集合文件按原顺序列出各项，不解析字形轮廓
// 返回: []FontFace 字体列表, error 格式或名称表错误
func (f FontFile) Faces() ([]FontFace, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ReadFaces(data)
}

// Face 提取指定字体，返回独立的OpenType数据，可用于预览或AddFont，且不修改原文件
// 入参: index 零起始字体索引，非集合文件只能为0
// 返回: []byte 独立字体数据, error 格式或索引错误
func (f FontFile) Face(index int) ([]byte, error) {
	data, err := decodeFontContainer(f.Data)
	if err != nil {
		return nil, err
	}
	return basefont.ExtractFace(data, index)
}

// MissingGlyphs 检查指定字体的字符覆盖，不注册资源或修改文档，忽略段落换行
// 入参: index 零起始字体索引, text 待检查文字
// 返回: string 按首次出现顺序去重的缺失字符, error 字体或文字格式错误
func (f FontFile) MissingGlyphs(index int, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("text must be UTF-8")
	}
	data, err := f.Face(index)
	if err != nil {
		return "", err
	}
	return basefont.MissingGlyphs(data, text)
}
