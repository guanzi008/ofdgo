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

package font

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Face 字体文件中的字体名称与零起始索引，不依赖文件名
type Face struct {
	Index          int      `json:"index"`
	FullName       string   `json:"fullName"`
	Family         string   `json:"family"`
	Style          string   `json:"style"`
	PostScriptName string   `json:"postscriptName"`
	Names          []string `json:"names"`
}

// FaceInfo 从名称表读取字体信息
// 入参: data 名称表数据, index 零起始字体索引
// 返回: Face 字体信息
func FaceInfo(data []byte, index int) Face {
	records, values := fontNameValues(data)
	name := func(ids ...uint16) string {
		for _, id := range ids {
			for _, record := range records {
				if record.Name == id {
					if value := strings.TrimSpace(values[[4]uint16{record.Platform, record.Encoding, record.Language, id}]); value != "" {
						return value
					}
				}
			}
		}
		return ""
	}
	return Face{Index: index, FullName: name(4, 6, 16, 1), Family: name(16, 1), Style: name(17, 2), PostScriptName: name(6), Names: NamesFromTable(data)}
}

// FileCount 校验字体或集合头并读取字体数量
// 入参: data OpenType数据
// 返回: int 字体数量, error 格式错误
func FileCount(data []byte) (int, error) {
	if len(data) < 12 {
		return 0, fmt.Errorf("invalid font header")
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
		return 1, nil
	case "ttcf":
		version := binary.BigEndian.Uint32(data[4:])
		count := uint64(binary.BigEndian.Uint32(data[8:]))
		if (version != 0x00010000 && version != 0x00020000) || count == 0 || count > uint64(len(data)-12)/4 {
			return 0, fmt.Errorf("invalid font collection header")
		}
		return int(count), nil
	default:
		return 0, fmt.Errorf("invalid OpenType header")
	}
}

// ReadFaces 读取已解封装字体或集合的名称信息
// 入参: data OpenType数据
// 返回: []Face 字体列表, error 格式或名称表错误
func ReadFaces(data []byte) ([]Face, error) {
	count, err := FileCount(data)
	if err != nil {
		return nil, err
	}
	faces := make([]Face, 0, count)
	for index := 0; index < count; index++ {
		tables, err := FileTables(data, index)
		if err != nil {
			return nil, err
		}
		face := FaceInfo(tables["name"], index)
		if face.FullName == "" {
			return nil, fmt.Errorf("font %d has no usable name", index)
		}
		faces = append(faces, face)
	}
	return faces, nil
}

// ExtractFace 提取已解封装字体中的集合项，规范字符映射并保留原始表
// 入参: data OpenType数据, index 零起始字体索引
// 返回: []byte 独立字体数据, error 格式或索引错误
func ExtractFace(data []byte, index int) ([]byte, error) {
	var err error
	if bytes.HasPrefix(data, []byte("ttcf")) {
		data, err = ExtractCollectionFont(data, index)
		if err != nil {
			return nil, err
		}
		index = 0
	}
	tables, err := FileTables(data, index)
	if err != nil {
		return nil, err
	}
	cmap, err := NormalizeCmap(tables["cmap"])
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(cmap, tables["cmap"]) {
		tables["cmap"] = cmap
		return SerializeOTF(tables)
	}
	return bytes.Clone(data), nil
}

// MissingGlyphs 检查独立字体的字符覆盖，忽略段落换行并按首次出现顺序去重
// 入参: data 独立OpenType数据, text 待检查文字
// 返回: string 缺失字符, error 字体或文字格式错误
func MissingGlyphs(data []byte, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", fmt.Errorf("text must be UTF-8")
	}
	data, err := DefaultCFF2Font(data)
	if err != nil {
		return "", err
	}
	sfnt, err := ParseSFNT(data)
	if err != nil {
		return "", err
	}
	seen := make(map[rune]bool)
	var missing strings.Builder
	for _, char := range text {
		if char == '\r' || char == '\n' || seen[char] {
			continue
		}
		seen[char] = true
		if sfnt.GlyphIndex(char) == 0 {
			missing.WriteRune(char)
		}
	}
	return missing.String(), nil
}
