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

	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
)

// appendFontFileNames 建立各字体的内部名称与文件别名匹配候选
// 入参: candidates 字体候选, index 文件候选索引, faces 各字体内部名称
// 返回: []fontFileCandidate 字体候选列表
func appendFontFileNames(candidates []fontFileCandidate, index int, faces [][]string) []fontFileCandidate {
	file := candidates[index]
	replaced := false
	for face, names := range faces {
		file.face = face
		file.names = make([]string, len(names))
		for i, name := range names {
			file.names[i] = basefont.NormalizeName(name)
		}
		for _, name := range file.names {
			file.normalized = name
			if !replaced {
				candidates[index] = file
				replaced = true
			} else {
				candidates = append(candidates, file)
			}
		}
	}
	return candidates
}

// fontCollectionIndex 按内部名称与样式选择集合项，未匹配时保留首项
// 入参: data 集合数据, names 字体名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func fontCollectionIndex(data []byte, names []string, bold, italic bool) int {
	return fontNameIndex(basefont.FileNames(bytes.NewReader(data)), names, bold, italic)
}

// fontNameIndex 按名称与样式选择字体索引，未匹配时保留首项
// 入参: faces 各字体名称, names 目标名称, bold 是否粗体, italic 是否斜体
// 返回: int 零起始字体索引
func fontNameIndex(faces [][]string, names []string, bold, italic bool) int {
	candidates := appendFontFileNames([]fontFileCandidate{{}}, 0, faces)
	if matches := fontFileMatches(candidates, names, bold, italic); len(matches) > 0 {
		return matches[0].face
	}
	return 0
}
