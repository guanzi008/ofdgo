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

// compactVersionFonts 清理仅由版本清单保留的无引用字体，同步清单且不修改编辑器快照
// 入参: ctx 取消上下文, parts 输出修改, removed 待删除条目
// 返回: error 读取、结构或取消错误
func (e *Editor) compactVersionFonts(ctx context.Context, parts map[string][]byte, removed map[string]bool) error {
	reader := e.source.reader
	versioned := false
	for _, body := range reader.OFD.DocBody {
		versioned = versioned || body.versioned
	}
	if !versioned {
		return nil
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
	if len(candidates) == 0 {
		return nil
	}
	if err := pruneExternalFontFiles(ctx, view, slices.Collect(maps.Keys(candidates))...); err != nil {
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
