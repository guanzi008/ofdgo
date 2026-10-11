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
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"slices"
)

// editorProfileReference 记录颜色配置文件所在的资源索引及颜色空间标识
type editorProfileReference struct {
	index string
	id    string
}

// compactResourceProfiles 合并相同颜色配置文件，保留颜色空间声明和独立文件引用
// 入参: ctx 取消上下文, reader 原包，可为空, parts 输出改动, profiles 配置文件引用, protected 不可替换的文件, removed 待移除文件
// 返回: error 读取、改写或取消错误
func compactResourceProfiles(ctx context.Context, reader *Reader, parts map[string][]byte, profiles map[string][]editorProfileReference, protected, removed map[string]bool) error {
	if len(profiles) < 2 {
		return ctx.Err()
	}
	seen := make(map[[32]byte]string)
	aliases := make(map[string]string)
	indexes := make(map[string]map[string]string)
	buffer := make([]byte, 32*1024)
	for _, name := range slices.Sorted(maps.Keys(profiles)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if protected[name] {
			continue
		}
		var input io.ReadCloser
		if data, ok := parts[name]; ok {
			input = io.NopCloser(bytes.NewReader(data))
		} else {
			if reader == nil {
				return fmt.Errorf("file not found: %s", name)
			}
			var err error
			input, err = reader.openFile(name)
			if err != nil {
				return err
			}
		}
		hash := sha256.New()
		_, err := io.CopyBuffer(hash, imageInput{ReadCloser: input, context: ctx}, buffer)
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		key := [32]byte(hash.Sum(nil))
		first, found := seen[key]
		if !found {
			seen[key] = name
			continue
		}
		aliases[name] = first
		for _, reference := range profiles[name] {
			if indexes[reference.index] == nil {
				indexes[reference.index] = make(map[string]string)
			}
			indexes[reference.index][reference.id] = first
		}
	}
	updates := make(map[string][]byte, len(indexes))
	for _, name := range slices.Sorted(maps.Keys(indexes)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, ok := parts[name]
		if !ok {
			if reader == nil {
				return fmt.Errorf("file not found: %s", name)
			}
			var err error
			data, err = reader.readFile(name)
			if err != nil {
				return err
			}
		}
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return err
		}
		var patches []editorXMLPatch
		for _, group := range root.children {
			if !group.matchesOFD("ColorSpaces") {
				continue
			}
			for _, node := range group.children {
				location := indexes[name][editorResourceID(node.attr("ID"))]
				if !node.matchesOFD("ColorSpace") || location == "" {
					continue
				}
				fragment, err := editorXMLAttribute(data, node, "Profile", "/"+location)
				if err != nil {
					return err
				}
				patches = append(patches, editorXMLPatch{start: node.start, end: node.end, data: fragment})
			}
		}
		updates[name] = editorPatchXML(data, patches)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	maps.Copy(parts, updates)
	for name := range aliases {
		removed[name] = true
		delete(parts, name)
	}
	return nil
}
