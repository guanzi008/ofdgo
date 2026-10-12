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
	"archive/zip"
	"context"
	"fmt"
)

// ExtractVersion 将当前文档的指定版本提取为独立阅读器，可继续编辑或另存，不改变原包
// 仅保留版本文件清单中的文件并重建单文档入口，不保留版本列表；原签名可能因入口变化而失效
// 文件内容按需读取，使用期间不得关闭持有输入文件的原阅读器；返回值不关闭输入文件
// 入参: id 版本标识，空值选择唯一的Current版本，没有默认版本或存在歧义时返回错误
// 返回: *Reader 版本阅读器, error 选择、读取或文件缺失错误
func (r *Reader) ExtractVersion(id string) (*Reader, error) {
	return r.ExtractVersionContext(context.Background(), id)
}

// ExtractVersionContext 将指定版本提取为独立阅读器，扫描、读取和解析时响应取消
// 文件保留范围、入口选择和输入文件生命周期与ExtractVersion相同，取消或失败不改变原包
// 入参: ctx 取消上下文, id 版本标识，空值选择唯一的Current版本
// 返回: *Reader 版本阅读器, error 选择、读取或取消错误
func (r *Reader) ExtractVersionContext(ctx context.Context, id string) (*Reader, error) {
	entries, err := r.documentVersionEntriesContext(ctx)
	if err != nil {
		return nil, err
	}
	var selected *DocumentVersionInfo
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := &entries[i]
		if id != "" && entry.ID == id || id == "" && entry.Current {
			if selected != nil {
				return nil, fmt.Errorf("multiple current document versions")
			}
			selected = entry
		}
	}
	if selected == nil {
		if id == "" {
			return nil, fmt.Errorf("no current document version")
		}
		return nil, fmt.Errorf("document version not found: %s", id)
	}
	version, err := r.readDocumentVersionContext(ctx, selected.ID, selected.Location)
	if err != nil {
		return nil, fmt.Errorf("version %s: %w", selected.ID, err)
	}
	files := make(map[string]bool, len(version.Files))
	for _, file := range version.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !r.fileNames[file.Location] {
			return nil, fmt.Errorf("version %s file not found: %s", selected.ID, file.Location)
		}
		files[file.Location] = true
	}
	root, err := r.extractedVersionRoot(ctx, version, files)
	if err != nil {
		return nil, err
	}
	next := &Reader{Path: r.Path, files: map[string][]byte{"OFD.xml": root}, encryption: r.encryption}
	for name := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if name != "OFD.xml" {
			if data, ok := r.files[name]; ok {
				next.files[name] = data
			}
		}
	}
	if r.Zip != nil {
		next.Zip = &zip.Reader{Comment: r.Zip.Comment}
		for _, file := range r.Zip.File {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			name := cleanPackagePath(file.Name)
			if name != "OFD.xml" && files[name] && !file.FileInfo().IsDir() {
				next.Zip.File = append(next.Zip.File, file)
			}
		}
	}
	if err := next.initRootContext(ctx); err != nil {
		return nil, err
	}
	if _, err := next.docStructureContext(ctx); err != nil {
		return nil, err
	}
	return next, nil
}

// extractedVersionRoot 重建版本副本入口，保留元数据及命名空间，移除其他文档与版本列表
// 入参: ctx 取消上下文, version 版本描述, files 版本包含的文件
// 返回: []byte 主入口XML, error 读取、结构或取消错误
func (r *Reader) extractedVersionRoot(ctx context.Context, version DocumentVersionInfo, files map[string]bool) ([]byte, error) {
	data, err := r.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return nil, err
	}
	body := root.childAt("DocBody", r.documentIndex)
	if body == nil {
		return nil, fmt.Errorf("document entry not found: %d", r.documentIndex)
	}
	var patches []editorXMLPatch
	for _, child := range root.children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if child.matchesOFD("DocBody") && child != body {
			patches = append(patches, editorXMLPatch{start: child.start, end: child.end})
		}
	}
	entry := body.child("DocRoot")
	if entry != nil {
		patches = append(patches, editorXMLContent(data, entry, editorFontXMLText(version.DocRoot)))
	}
	if versions := body.child("Versions"); versions != nil {
		var replacement []byte
		if entry == nil {
			replacement = body.textXML("DocRoot", version.DocRoot)
		}
		patches = append(patches, editorXMLPatch{versions.start, versions.end, replacement})
	} else if entry == nil {
		return nil, fmt.Errorf("document entry not found: %d", r.documentIndex)
	}
	references := []*editorXML{body.child("Signatures")}
	if info := body.child("DocInfo"); info != nil {
		references = append(references, info.child("Cover"))
	}
	for _, node := range references {
		if node == nil {
			continue
		}
		name := resolveResourcePath("OFD.xml", "", editorImportText(data, node))
		if files[name] {
			patches = append(patches, editorXMLContent(data, node, editorFontXMLText("/"+name)))
		} else {
			patches = append(patches, editorXMLPatch{start: node.start, end: node.end})
		}
	}
	data = editorPatchXML(data, patches)
	if version.Version != "" {
		root, err = parseEditorXMLContext(ctx, data)
		if err != nil {
			return nil, err
		}
		data, err = editorXMLAttribute(data, root, "Version", version.Version)
	}
	if err == nil {
		err = ctx.Err()
	}
	return data, err
}
