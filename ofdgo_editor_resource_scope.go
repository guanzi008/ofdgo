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
	"maps"
	"slices"
)

// resourceReferenceScopes 复用结构依赖归集文档引用，同文档索引共用只读结果，跨文档索引独立合并
// 入参: ctx 取消上下文, documents 文档入口, resources 资源索引, references 文件引用, selected 限定的文档入口，空值统计全部文档
// 返回: map[string]*editorResourceRefs 各资源索引的引用，依赖不完整或引用无法归属时为空, error 取消错误
func resourceReferenceScopes(ctx context.Context, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, selected string) (map[string]*editorResourceRefs, error) {
	indexes := make(map[string]bool, len(resources))
	for _, name := range resources {
		indexes[name] = true
	}
	used := make(map[string]bool)
	result := make(map[string]*editorResourceRefs)
	shared := make(map[string]bool)
	for _, entry := range slices.Sorted(maps.Keys(documents)) {
		scope := map[string]bool{entry: true}
		queue := []string{entry}
		refs := &editorResourceRefs{ids: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
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
		for name := range scope {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !indexes[name] {
				continue
			}
			if result[name] == nil {
				result[name] = refs
				continue
			}
			if !shared[name] {
				merged := &editorResourceRefs{ids: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
				merged.merge(result[name])
				result[name] = merged
				shared[name] = true
			}
			result[name].merge(refs)
		}
	}
	for name, refs := range references {
		if !used[name] && (len(refs.ids) != 0 || len(refs.fonts) != 0 || indexes[name]) {
			return nil, nil
		}
	}
	return result, ctx.Err()
}
