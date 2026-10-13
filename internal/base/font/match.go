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
	"path"
	"strings"
)

// fontNameReplacer 移除字体名称匹配时忽略的分隔符
var fontNameReplacer = strings.NewReplacer(" ", "", "-", "", "_", "", "(", "", ")", "", "（", "", "）", "")

// NormalizeName 规范化字体名称
// 入参: name 字体名称
// 返回: string 规范化后的字体名称
func NormalizeName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(strings.TrimSpace(name))
	if IsFontFileName(name) {
		name = strings.TrimSuffix(name, path.Ext(name))
	}
	name = strings.ToLower(strings.TrimSpace(name))
	return fontNameReplacer.Replace(name)
}

// IsFontFileName 判断是否为字体文件名
// 入参: name 文件名
// 返回: bool 是否为字体文件
func IsFontFileName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".ttf", ".otf", ".ttc":
		return true
	default:
		return false
	}
}

// AppendFontName 追加字体名称
// 入参: names 字体名称列表, seen 去重映射, name 字体名称
// 返回: []string 字体名称列表
func AppendFontName(names []string, seen map[string]bool, name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return names
	}
	key := NormalizeName(name)
	if key == "" || seen[key] {
		return names
	}
	seen[key] = true
	return append(names, name)
}
