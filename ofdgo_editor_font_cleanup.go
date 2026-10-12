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
	"fmt"
	"io"
	"maps"
	"slices"
)

// compactPackageFonts 清理无引用字体，补齐多文档及历史版本的字体裁剪，不修改编辑器快照
// 入参: ctx 取消上下文, parts 输出修改, removed 待删除条目, generated 本次编码的页面引用
// 返回: error 读取、结构或取消错误
func (e *Editor) compactPackageFonts(ctx context.Context, parts map[string][]byte, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	reader := e.source.reader
	versioned := false
	for _, body := range reader.OFD.DocBody {
		versioned = versioned || body.versioned
	}
	if !versioned {
		if reader.DocumentCount() <= 1 || e.backends.FontResources == nil || e.output != nil && e.output.options.Mode != CompressionUnchanged {
			return nil
		}
	}
	protected, err := reader.hasOutputSignatures(ctx)
	if err != nil || protected {
		return err
	}
	view := &Reader{Zip: reader.Zip, files: maps.Clone(reader.files)}
	if view.files == nil {
		view.files = make(map[string][]byte)
	}
	maps.Copy(view.files, parts)
	if err := view.initRootContext(ctx); err != nil {
		return err
	}
	data, err := view.readFileViewContext(ctx, "OFD.xml")
	if err != nil {
		return err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return err
	}
	candidates, seen := make(map[string]bool), make(map[string]bool)
	for _, body := range root.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !body.matchesOFD("DocBody") {
			continue
		}
		versions, err := parseVersionEntries(ctx, body)
		if err != nil {
			return err
		}
		for _, entry := range versions {
			version, err := view.readDocumentVersionContext(ctx, entry.ID, entry.Location)
			if err != nil {
				return fmt.Errorf("version %s: %w", entry.ID, err)
			}
			for _, file := range version.Files {
				if err := ctx.Err(); err != nil {
					return err
				}
				name := file.Location
				if seen[name] || removed[name] {
					continue
				}
				seen[name] = true
				input, err := view.openFile(name)
				if err != nil {
					return err
				}
				var header [4]byte
				n, err := io.ReadFull(input, header[:])
				input.Close()
				if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
					return err
				}
				if n == len(header) {
					switch string(header[:]) {
					case "\x00\x01\x00\x00", "OTTO", "ttcf":
						candidates[name] = true
					}
				}
			}
		}
	}
	if err := pruneExternalFontFiles(ctx, view, slices.Collect(maps.Keys(candidates))...); err != nil {
		return err
	}
	if err := e.subsetPackageFonts(ctx, view, removed, generated); err != nil {
		return err
	}
	for name, data := range view.files {
		if original, ok := reader.files[name]; !ok || !bytes.Equal(original, data) {
			parts[name] = data
		}
	}
	for name := range candidates {
		if !view.fileNames[name] {
			removed[name] = true
			delete(parts, name)
		}
	}
	return nil
}

// subsetPackageFonts 按文档作用域收集用字，合并共享字体需求并保留字形编号
// 入参: ctx 取消上下文, view 独立输出快照, removed 待删除条目, generated 本次编码的页面引用
// 返回: error 读取、裁剪或取消错误
func (e *Editor) subsetPackageFonts(ctx context.Context, view *Reader, removed map[string]bool, generated map[string]editorGeneratedReferences) error {
	if e.backends.FontResources == nil {
		return nil
	}
	names := maps.Clone(view.fileNames)
	for name := range removed {
		delete(names, name)
	}
	progress := editorProgress(e.OnWriteProgress)
	refs, resources, references, safe, err := scanEditorResources(ctx, view, view.files, generated, names, progress, true)
	if err != nil || !safe {
		return err
	}
	scoped, err := resourceReferenceScopes(ctx, refs.documents, resources, references, "")
	if err != nil || scoped == nil {
		return err
	}
	read := func(name string) ([]byte, error) { return view.readFileViewContext(ctx, name) }
	usage, err := resourceFontUsage(ctx, resources, scoped, read)
	if err != nil {
		return err
	}
	for _, source := range references {
		for name := range source.files {
			if font := usage[name]; font != nil && !source.versions[name] {
				font.unsafe = true
			}
		}
		for _, definition := range source.definitions {
			for name := range definition.files {
				if font := usage[name]; font != nil {
					font.unsafe = true
				}
			}
		}
	}
	var cache editorSourceFontCache
	for index, name := range slices.Sorted(maps.Keys(usage)) {
		if err := progress.report("fonts", index, len(usage)); err != nil {
			return err
		}
		if usage[name].unsafe || len(usage[name].chars)+len(usage[name].glyphs) == 0 {
			continue
		}
		data, err := read(name)
		if err != nil {
			return err
		}
		subset, err := cache.subset(ctx, e.backends.FontResources, data, usage[name])
		if err != nil {
			return err
		}
		if subset != nil {
			view.files[name] = subset
		}
	}
	if err := progress.report("fonts", len(usage), len(usage)); err != nil {
		return err
	}
	return ctx.Err()
}
