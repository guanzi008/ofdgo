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
	"context"
	"io"
	"maps"
	"slices"
	"strings"
)

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
		entry, err := reader.DocumentRoot()
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
			resource, safe = local.scan(imageInput{ReadCloser: input, context: ctx}, name)
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
		if collect && (resource || len(local.ids) != 0 || len(local.fonts) != 0) {
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
