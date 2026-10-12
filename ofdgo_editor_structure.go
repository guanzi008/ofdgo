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
	"encoding/xml"
	"fmt"
	"path"
	"strings"
)

// pruneStructureReferences 清理本次编辑删除的公文图元引用，未知标引和原有悬空引用保持原样
// 改动进入输出快照，由既有文档及版本隔离流程维护共享文件
// 入参: parts 输出覆盖条目
// 返回: error 读取、解析或取消错误
func (e *Editor) pruneStructureReferences(parts map[string][]byte) error {
	if e.source.document.CustomTags.Path == "" && len(e.source.document.CustomTags.CustomTag) == 0 {
		return nil
	}
	ctx := context.Background()
	if e.output != nil {
		ctx = e.output.ctx
	}
	pages := make(map[string]bool, len(e.pages))
	for _, page := range e.pages {
		pages[editorResourceID(page.ID)] = true
	}
	removedPages := make(map[string]bool)
	removedObjects := make(map[string]map[string]bool)
	for id, source := range e.source.pages {
		key := editorResourceID(id)
		if !pages[key] {
			removedPages[key] = true
			continue
		}
		data, changed := parts[e.source.reader.ResPath(source.ref.BaseLoc)]
		if !changed || source.original == nil {
			continue
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		remaining := make(map[string]bool)
		var scan func(*editorXML)
		scan = func(node *editorXML) {
			if classifyOFDNamespace(node.name.Space) == ofdXMLUnknown {
				return
			}
			if id := editorResourceID(node.attr("ID")); id != "" {
				remaining[id] = true
			}
			for _, child := range node.children {
				scan(child)
			}
		}
		scan(root)
		for id := range source.nodes {
			id = editorResourceID(id)
			if id != "" && !remaining[id] {
				if removedObjects[key] == nil {
					removedObjects[key] = make(map[string]bool)
				}
				removedObjects[key][id] = true
			}
		}
	}
	if len(removedPages) == 0 && len(removedObjects) == 0 {
		return nil
	}
	tags, err := e.source.reader.CustomTags()
	if err != nil {
		return err
	}
	for _, tag := range tags {
		if !structureOfficial(tag, tag.NameSpace) {
			continue
		}
		name := e.source.reader.ResPath(tag.FileLoc)
		data, ok := parts[name]
		if !ok {
			data, err = e.source.reader.readFileViewContext(ctx, name)
			if err != nil {
				return err
			}
		}
		updated, err := rewriteStructureReferences(ctx, data, tag, func(page string, ids []string) (string, []string) {
			key := editorResourceID(page)
			if removedPages[key] {
				return page, nil
			}
			kept := make([]string, 0, len(ids))
			for _, id := range ids {
				if !removedObjects[key][editorResourceID(id)] {
					kept = append(kept, id)
				}
			}
			return page, kept
		})
		if err != nil {
			return err
		}
		if !bytes.Equal(data, updated) {
			parts[name] = updated
		}
	}
	return nil
}

// rewriteStructureReferences 仅改写已识别公文命名空间的叶子引用，保留其他XML字节
// 入参: ctx 取消上下文, data 原始标引, tag 标引入口, rewrite 引用映射，空标识列表表示删除引用
// 返回: []byte 修改后的标引, error 解析或取消错误
func rewriteStructureReferences(ctx context.Context, data []byte, tag CustomTag, rewrite func(string, []string) (string, []string)) ([]byte, error) {
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	if !structureOfficial(tag, root.name.Space) {
		return data, nil
	}
	var patches []editorXMLPatch
	count := 0
	var walk func(*editorXML, int) error
	walk = func(node *editorXML, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if depth > 128 || count > 100000 {
			return fmt.Errorf("custom tag structure limit exceeded")
		}
		if node.name.Space != root.name.Space {
			return nil
		}
		if node.name.Local == "ObjectRef" && len(node.children) == 0 {
			value := strings.TrimSpace(editorImportText(data, node))
			ids := structureObjectIDs(value)
			if len(ids) == 0 {
				return nil
			}
			page, next := rewrite(node.attr("PageRef"), ids)
			if len(next) == 0 {
				patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
				return nil
			}
			if strings.Join(next, ",") != strings.Join(ids, ",") {
				var text bytes.Buffer
				_ = xml.EscapeText(&text, []byte(strings.Join(next, ",")))
				patches = append(patches, editorXMLContent(data, node, text.Bytes()))
			}
			if page != node.attr("PageRef") {
				patch, err := editorXMLAttributePatch(data, node, "PageRef", page)
				if err != nil {
					return err
				}
				patches = append(patches, patch)
			}
			return nil
		}
		for _, child := range node.children {
			if err := walk(child, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, 1); err != nil {
		return nil, err
	}
	if len(patches) == 0 {
		return data, nil
	}
	return editorPatchXML(data, patches), nil
}

// structures 迁移所选页面的公文标引，按已迁移图元映射引用，不猜测其他行业的引用规则
// 返回: []byte 标引索引条目, error 读取、解析或取消错误
func (m *editorPageImport) structures() ([]byte, error) {
	tags, err := m.reader.CustomTags()
	if err != nil {
		return nil, err
	}
	var entries []byte
	for i, tag := range tags {
		if !structureOfficial(tag, tag.NameSpace) {
			continue
		}
		data, err := m.readFile(m.reader.ResPath(tag.FileLoc))
		if err != nil {
			return nil, err
		}
		count := 0
		updated, err := rewriteStructureReferences(m.ctx, data, tag, func(page string, ids []string) (string, []string) {
			key := editorImportKey(page)
			if !m.pages[key] {
				return page, nil
			}
			var mapped []string
			for _, id := range ids {
				if value := m.ids[editorImportKey(id)]; value != "" {
					mapped = append(mapped, value)
				}
			}
			count += len(mapped)
			return m.ids[key], mapped
		})
		if err != nil {
			return nil, err
		}
		if count == 0 {
			continue
		}
		name := packageAvailableName(m.target, m.files, path.Join(m.directory, "Tags", fmt.Sprintf("Tag_%d.xml", i)))
		m.files[name] = updated
		var content []byte
		if tag.SchemaLoc != "" {
			schema, err := m.copyData(m.reader.ResPath(tag.SchemaLoc), path.Join(m.directory, "Tags", path.Base(tag.SchemaLoc)), false)
			if err != nil {
				return nil, err
			}
			content = append(content, editorXMLText("SchemaLoc", "/"+strings.TrimPrefix(schema, "/"))...)
		}
		content = append(content, editorXMLText("FileLoc", "/"+name)...)
		var attrs ofdAttrs
		attrs.add("TypeID", tag.TypeID)
		attrs.add("NameSpace", tag.NameSpace)
		entry, err := editorXMLContainer("CustomTag", attrs, content)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry...)
	}
	return entries, nil
}
