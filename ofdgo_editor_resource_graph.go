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
	"io"
)

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
	scan := editorReferenceScan{refs: outer, name: name, safe: true}
	decoder := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
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
			if depth == 0 && (scan.seen || node.Name.Local != "Res") || depth == 1 && editorResourceElement(node.Name.Local) == "" {
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
