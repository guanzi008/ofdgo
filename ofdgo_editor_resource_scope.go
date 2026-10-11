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

// editorResourceScope 记录资源索引的文档归属链，引用集合只读
type editorResourceScope struct {
	parent int
	refs   *editorResourceRefs
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
