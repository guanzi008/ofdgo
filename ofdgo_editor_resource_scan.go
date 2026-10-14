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
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"maps"
	"slices"
	"strings"
)

// editorResourceScope 记录资源索引的文档归属链，引用集合只读
type editorResourceScope struct {
	parent int
	refs   *editorResourceRefs
}

// scanEditorResources 优先扫描结构依赖，原始数据不按XML解析，孤立文件仍保留引用检查
// 入参: ctx 取消上下文, reader 原包, parts 输出改动, generated 自产引用, names 扫描范围, progress 扫描进度, collect 是否保留各文件的资源引用
// 返回: *editorResourceRefs 全部引用, []string 资源索引, map[string]*editorResourceRefs 文件引用, bool 是否可安全优化, error 读取或取消错误
func scanEditorResources(ctx context.Context, reader *Reader, parts map[string][]byte, generated map[string]editorGeneratedReferences, names map[string]bool, progress editorProgress, collect bool) (*editorResourceRefs, []string, map[string]*editorResourceRefs, bool, error) {
	refs := &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage), roles: make(map[string]int), documents: make(map[string]bool), versions: make(map[string]bool)}
	var resources []string
	var references map[string]*editorResourceRefs
	if collect {
		references = make(map[string]*editorResourceRefs)
	}
	ordered := slices.Sorted(maps.Keys(names))
	actual := make(map[string]string, len(names))
	for _, name := range ordered {
		key := cleanPackagePath(name)
		if prior, ok := actual[key]; ok && prior != name {
			return nil, nil, nil, false, nil
		}
		actual[key] = name
	}
	queue := []string{"OFD.xml"}
	if reader != nil && reader.OFD.DocBody[reader.documentIndex].versioned {
		entry, err := reader.DocumentRootContext(ctx)
		if err != nil {
			return nil, nil, nil, false, err
		}
		queue = append(queue, entry)
	}
	seen := make(map[string]bool)
	sharedRefs := *refs
	position, pending, completed := 0, 0, 0
	for pending < len(queue) || position < len(ordered) {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, false, err
		}
		var key string
		if pending < len(queue) {
			key = cleanPackagePath(queue[pending])
			pending++
		} else {
			key = cleanPackagePath(ordered[position])
			position++
		}
		name, included := actual[key]
		if !included {
			if key != "OFD.xml" || reader == nil {
				continue
			}
			name = key
		}
		if seen[key] || strings.HasSuffix(name, "/") || refs.roles[key] == packageReferenceData {
			continue
		}
		seen[key] = true
		if included {
			if err := progress.report("references", completed, len(ordered)); err != nil {
				return nil, nil, nil, false, err
			}
			completed++
		}
		data, replaced := parts[name]
		known := generated[name]
		local := known.refs
		resource, safe, shared := false, true, false
		if !replaced || local == nil || !known.matches(data) {
			shared = included && !collect
			if shared {
				sharedRefs.roles = nil
				local = &sharedRefs
			} else {
				local = &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
			}
			var input io.ReadCloser
			if replaced {
				if data == nil && known.staged != nil {
					input = known.staged.open()
				} else {
					input = io.NopCloser(bytes.NewReader(data))
				}
			} else {
				var err error
				input, err = reader.openFile(name)
				if err != nil {
					return nil, nil, nil, false, err
				}
			}
			resource, safe = local.scanContext(ctx, input, name)
			input.Close()
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, false, err
		}
		if !safe {
			return nil, nil, nil, false, nil
		}
		for _, target := range slices.Sorted(maps.Keys(local.roles)) {
			role := local.roles[target]
			refs.roles[target] |= role
			if role&packageReferenceXML != 0 {
				queue = append(queue, target)
			}
		}
		if !included {
			continue
		}
		if !shared {
			refs.merge(local)
		}
		if resource {
			resources = append(resources, name)
		}
		if collect {
			references[name] = local
		}
	}
	for name, role := range refs.roles {
		if role&packageReferenceData != 0 && seen[name] && actual[name] != "" {
			return nil, nil, nil, false, nil
		}
	}
	if err := progress.report("references", len(ordered), len(ordered)); err != nil {
		return nil, nil, nil, false, err
	}
	return refs, resources, references, true, ctx.Err()
}

// editorResourceElement 返回标准资源集合中的声明元素名称
// 入参: group 资源集合名称
// 返回: string 声明元素名称，未知集合返回空值
func editorResourceElement(group string) string {
	switch group {
	case "Fonts":
		return "Font"
	case "MultiMedias":
		return "MultiMedia"
	case "ColorSpaces":
		return "ColorSpace"
	case "DrawParams":
		return "DrawParam"
	case "CompositeGraphicUnits":
		return "CompositeGraphicUnit"
	}
	return ""
}

// editorResourceDefinitions 单次流式读取资源声明依赖，保留历史索引和非图像媒体
// 入参: ctx 取消上下文, data 资源索引内容, name 文件路径, source 扫描结果, preserved 是否保留全部声明
// 返回: *editorResourceRefs 含声明依赖的引用，无法安全分析时为空, error 解析或取消错误
func editorResourceDefinitions(ctx context.Context, data []byte, name string, source *editorResourceRefs, preserved bool) (*editorResourceRefs, error) {
	result := *source
	result.ids, result.fonts = make(map[string]bool), nil
	result.definitions = make(map[string]*editorResourceRefs)
	outer := &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
	scan := editorReferenceScan{ctx: ctx, refs: outer, name: name, safe: true}
	input := bufio.NewReader(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
	if found, err := editorXMLPrefix(input); err != nil || !found {
		return nil, err
	}
	decoder := xml.NewDecoder(input)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			if !scan.seen || len(scan.stack) != 0 {
				return nil, nil
			}
			return &result, ctx.Err()
		}
		if err != nil {
			return nil, err
		}
		depth := len(scan.stack)
		if node, ok := token.(xml.StartElement); ok {
			if depth == 0 && node.Name.Local != "Res" || depth == 1 && editorResourceElement(node.Name.Local) == "" {
				return nil, nil
			}
			if depth == 2 {
				kind := editorResourceElement(scan.stack[1])
				var id, mediaType string
				for _, attr := range node.Attr {
					if attr.Name.Space == "" {
						switch attr.Name.Local {
						case "ID":
							id = editorResourceID(attr.Value)
						case "Type":
							mediaType = attr.Value
						}
					}
				}
				if node.Name.Local != kind || id == "" || result.definitions[id] != nil {
					return nil, nil
				}
				refs := &editorResourceRefs{ids: make(map[string]bool), files: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
				result.definitions[id] = refs
				scan.refs = refs
				if preserved || kind == "MultiMedia" && mediaType != "Image" {
					result.ids[id] = true
				}
			}
		}
		if !scan.accept(token) {
			return nil, ctx.Err()
		}
		if _, end := token.(xml.EndElement); end && depth == 3 {
			scan.refs = outer
		}
	}
}

// resourceReferenceScopes 按文档归集引用，归属相同的资源索引复用只读结果
// 入参: ctx 取消上下文, documents 文档入口, resources 资源索引, references 文件引用, selected 限定的文档入口，空值统计全部文档
// 返回: map[string]*editorResourceRefs 各资源索引的引用，依赖不完整或引用无法归属时为空, error 取消错误
func resourceReferenceScopes(ctx context.Context, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, selected string) (map[string]*editorResourceRefs, error) {
	indexes := make(map[string]bool, len(resources))
	for _, name := range resources {
		indexes[name] = true
	}
	used := make(map[string]bool)
	owners := make(map[string]int)
	groups := []editorResourceScope{{}}
	groupIDs := make(map[editorResourceScope]int)
	for _, entry := range slices.Sorted(maps.Keys(documents)) {
		scope := map[string]bool{entry: true}
		queue := []string{entry}
		refs := &editorResourceRefs{ids: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
		definitions := make(map[string]*editorResourceRefs)
		for position := 0; position < len(queue); position++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := queue[position]
			source := references[name]
			if source == nil {
				return nil, ctx.Err()
			}
			used[name] = true
			if selected == "" || selected == entry {
				refs.merge(&editorResourceRefs{ids: source.ids, fonts: source.fonts})
				for id, definition := range source.definitions {
					if definitions[id] != nil {
						return nil, nil
					}
					definitions[id] = definition
				}
			}
			for target, role := range source.roles {
				if role&packageReferenceXML != 0 && !scope[target] {
					scope[target] = true
					queue = append(queue, target)
				}
			}
		}
		if selected != "" && selected != entry {
			continue
		}
		var pending []string
		if len(definitions) != 0 {
			pending = slices.Collect(maps.Keys(refs.ids))
		}
		for position := 0; position < len(pending); position++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if definition := definitions[pending[position]]; definition != nil {
				for id := range definition.ids {
					if !refs.ids[id] {
						pending = append(pending, id)
					}
				}
				refs.merge(&editorResourceRefs{ids: definition.ids, fonts: definition.fonts})
			}
		}
		for name := range scope {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !indexes[name] {
				continue
			}
			key := editorResourceScope{parent: owners[name], refs: refs}
			id := groupIDs[key]
			if id == 0 {
				id = len(groups)
				groups = append(groups, key)
				groupIDs[key] = id
			}
			owners[name] = id
		}
	}
	for name, refs := range references {
		if !used[name] && (len(refs.ids) != 0 || len(refs.fonts) != 0 || indexes[name]) {
			return nil, nil
		}
	}
	result := make(map[string]*editorResourceRefs, len(owners))
	merged := make(map[int]*editorResourceRefs)
	for name, id := range owners {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		refs := merged[id]
		if refs == nil {
			refs = groups[id].refs
			if groups[id].parent != 0 {
				refs = &editorResourceRefs{ids: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
				for next := id; next != 0; next = groups[next].parent {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					refs.merge(groups[next].refs)
				}
			}
			merged[id] = refs
		}
		result[name] = refs
	}
	return result, ctx.Err()
}
