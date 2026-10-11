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

// resourceReferenceScopes 按文档归集资源标识和字体用字，同文档索引共用只读结果，跨文档索引独立合并
// 入参: ctx 取消上下文, parts 输出改动, generated 自产页面引用, documents 文档入口, resources 资源索引, references 文件引用, selected 限定的文档入口，空值统计全部文档
// 返回: map[string]*editorResourceRefs 各资源索引的引用，存在无法归属的引用时为空, error 读取或取消错误
func (r *Reader) resourceReferenceScopes(ctx context.Context, parts map[string][]byte, generated map[string]editorGeneratedReferences, documents map[string]bool, resources []string, references map[string]*editorResourceRefs, selected string) (map[string]*editorResourceRefs, error) {
	indexes := make(map[string]bool, len(resources))
	for _, name := range resources {
		indexes[name] = true
	}
	used := make(map[string]bool)
	result := make(map[string]*editorResourceRefs)
	shared := make(map[string]bool)
	for _, entry := range slices.Sorted(maps.Keys(documents)) {
		scope, err := r.versionFileDependencies(ctx, entry, parts, generated)
		if err != nil {
			return nil, err
		}
		refs := &editorResourceRefs{ids: make(map[string]bool), fonts: make(map[string]*editorFontUsage)}
		for name := range scope {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if source := references[name]; source != nil {
				used[name] = true
				if selected == "" || selected == entry {
					refs.merge(&editorResourceRefs{ids: source.ids, fonts: source.fonts})
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
