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
	"encoding/xml"
	"strings"
)

// compressionFontUsage 按文档合并共享字体用字，版本编辑仅统计选中入口
// 入参: ctx 取消上下文, documents 文档入口, resources 资源索引, references 各文件引用, read 输出文件读取方法
// 返回: map[string]*editorFontUsage 各字体文件的用字，无法确定作用域时为空, error 读取或取消错误
func (r *Reader) compressionFontUsage(ctx context.Context, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, read func(string) ([]byte, error)) (map[string]*editorFontUsage, error) {
	selected := ""
	if r.OFD.DocBody[r.documentIndex].versioned {
		var err error
		selected, err = r.DocumentRoot()
		if err != nil {
			return nil, err
		}
	}
	scoped, err := resourceReferenceScopes(ctx, documents, resources, references, selected)
	if err != nil || scoped == nil {
		return nil, err
	}
	result := make(map[string]*editorFontUsage)
	for _, name := range resources {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		refs := scoped[name]
		if refs == nil {
			continue
		}
		data, err := read(name)
		if err != nil {
			return nil, err
		}
		root, err := parseEditorXML(data)
		if err != nil {
			return nil, err
		}
		fonts := root.child("Fonts")
		if fonts == nil {
			continue
		}
		for _, font := range fonts.children {
			file := font.child("FontFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			location := editorResourceLocation(name, root.attr("BaseLoc"), value)
			id, charset := editorResourceID(font.attr("ID")), font.attr("Charset")
			unsafe := id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
			if result[location] == nil {
				result[location] = newEditorFontUsage()
			}
			result[location].merge(refs.fonts[id], unsafe)
		}
	}
	return result, ctx.Err()
}
