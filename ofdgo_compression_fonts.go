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
		selected, err = r.DocumentRootContext(ctx)
		if err != nil {
			return nil, err
		}
	}
	scoped, err := resourceReferenceScopes(ctx, documents, resources, references, selected)
	if err != nil || scoped == nil {
		return nil, err
	}
	return resourceFontUsage(ctx, resources, scoped, read)
}

// resourceFontUsage 按已确认的文档作用域合并各字体文件的字符和字形引用
// 入参: ctx 取消上下文, resources 资源索引, scoped 各索引的用字范围, read 输出文件读取方法
// 返回: map[string]*editorFontUsage 各字体文件的用字, error 读取或取消错误
func resourceFontUsage(ctx context.Context, resources []string, scoped map[string]*editorResourceRefs, read func(string) ([]byte, error)) (map[string]*editorFontUsage, error) {
	result := make(map[string]*editorFontUsage)
	protected := make(map[string]bool)
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
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		for font := range editorResourceNodes(root, "Fonts", "Font") {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			file := font.child("FontFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			location := editorResourceLocation(name, root.attr("BaseLoc"), value)
			alternate := cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))
			id, charset := editorResourceID(font.attr("ID")), font.attr("Charset")
			unsafe := id == "" || charset != "" && !strings.EqualFold(charset, "unicode") || location != alternate
			for _, path := range []string{location, alternate} {
				if result[path] == nil {
					result[path] = newEditorFontUsage()
				}
				result[path].merge(refs.fonts[id], unsafe)
			}
		}
		for media := range editorResourceNodes(root, "MultiMedias", "MultiMedia") {
			file := media.child("MediaFile")
			if file == nil {
				continue
			}
			var value string
			if err := xml.Unmarshal(data[file.start:file.end], &value); err != nil {
				return nil, err
			}
			for _, location := range []string{editorResourceLocation(name, root.attr("BaseLoc"), value), cleanPackagePath(resolveResourcePath(name, root.attr("BaseLoc"), value))} {
				protected[location] = true
			}
		}
	}
	for location := range protected {
		if usage := result[location]; usage != nil {
			usage.unsafe = true
		}
	}
	return result, ctx.Err()
}
