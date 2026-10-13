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
	"fmt"
	"io"
	"maps"
	"path"
	"slices"
	"strings"
)

// ResourceInfo 描述当前文档及所选版本的字体、图片、音视频或附件，Size为文件字节数
// Kind为Font、Image、Audio、Video或Attachment，标识仅在当前文档及同类资源内有效
// Complete表示已完整识别引用；不包含其他文档和历史版本的引用，空File表示外部字体
// Missing表示声明的文件不在包内，与外部字体及零字节文件区分，不表示文件解码结果
type ResourceInfo struct {
	Kind       string              `json:"kind"`
	ID         string              `json:"id"`
	Name       string              `json:"name"`
	Format     string              `json:"format"`
	File       string              `json:"file"`
	Size       uint64              `json:"size"`
	Missing    bool                `json:"missing"`
	Complete   bool                `json:"complete"`
	References []ResourceReference `json:"references"`
}

// ResourceReference 记录资源引用所在的XML及字段，Page为零基页码，非页面文件为-1
// Object为最近的图元标识，模板、注解、图案和复合资源通过File区分，不推断其页面实例数
type ResourceReference struct {
	File   string `json:"file"`
	Page   int    `json:"page"`
	Object string `json:"object"`
	Role   string `json:"role"`
}

// resourceCatalog 保存当前资源声明和原文位置，供查询及隔离修改复用
type resourceCatalog struct {
	scan  *editorFontDocument
	items []ResourceInfo
	nodes map[string]editorFontText
}

// Resources 列出当前文档及所选版本的资源，标记缺失文件，不读取二进制内容或执行媒体动作
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 资源及引用位置, error 结构或读取错误
func (r *Reader) Resources(ctx context.Context) ([]ResourceInfo, error) {
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return catalog.items, nil
}

// OpenResource 打开当前资源的原始文件，外部字体无可导出的内嵌文件
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: io.ReadCloser 文件流，由调用方关闭, error 资源或文件错误
func (r *Reader) OpenResource(ctx context.Context, kind, id string) (io.ReadCloser, error) {
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	item, err := catalog.find(kind, id)
	if err != nil {
		return nil, err
	}
	if item.File == "" {
		return nil, fmt.Errorf("resource has no embedded file")
	}
	stream, err := r.openFile(item.File)
	if err != nil {
		return nil, err
	}
	return imageInput{ReadCloser: stream, context: ctx}, nil
}

// resourceCatalog 按标准内容路径收集资源声明及引用，不把任意数字属性作为资源引用
// 入参: ctx 取消上下文
// 返回: *resourceCatalog 资源索引, error 结构或读取错误
func (r *Reader) resourceCatalog(ctx context.Context) (*resourceCatalog, error) {
	scan, err := scanEditorFonts(ctx, r, r.DocumentIndex())
	if err != nil {
		return nil, err
	}
	catalog := &resourceCatalog{scan: scan, nodes: make(map[string]editorFontText), items: []ResourceInfo{}}
	pages := make(map[string]int)
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	for i, page := range doc.Pages.Page {
		pages[cleanPackagePath(r.ResPath(page.BaseLoc))] = i
	}
	refs := make(map[string][]ResourceReference)
	for _, name := range slices.Sorted(maps.Keys(scan.files)) {
		file := scan.files[name]
		root, err := parseEditorXMLContext(ctx, file.data)
		if err != nil {
			return nil, err
		}
		if root.matchesOFD("Res") {
			for _, group := range root.children {
				for _, node := range group.children {
					item := ResourceInfo{ID: editorResourceID(node.attr("ID")), Complete: !scan.unsafe}
					field := ""
					if group.matchesOFD("Fonts") && node.matchesOFD("Font") {
						item.Kind, item.Name, field = "Font", node.attr("FontName"), "FontFile"
					} else if group.matchesOFD("MultiMedias") && node.matchesOFD("MultiMedia") && slices.Contains([]string{"Image", "Audio", "Video"}, node.attr("Type")) {
						item.Kind, item.Format, field = node.attr("Type"), node.attr("Format"), "MediaFile"
					} else {
						continue
					}
					if item.ID == "" {
						return nil, fmt.Errorf("invalid resource ID")
					}
					if target := node.child(field); target != nil {
						location := strings.TrimSpace(editorImportText(file.data, target))
						if location != "" {
							item.File = resolveResourcePath(name, root.attr("BaseLoc"), location)
						}
					}
					if item.Name == "" {
						item.Name = path.Base(item.File)
					}
					if err := catalog.add(item, editorFontText{file: file, node: node}); err != nil {
						return nil, err
					}
				}
			}
		}
		page := -1
		if index, ok := pages[name]; ok {
			page = index
		}
		var visit func(*editorXML, string) error
		visit = func(node *editorXML, object string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !node.matchesOFD(node.name.Local) || node.matchesOFD("Attachments") {
				return nil
			}
			if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject"}, node.name.Local) {
				object = node.attr("ID")
			}
			add := func(role, id string) {
				key := editorResourceID(id)
				if role == "AttachID" {
					key = "Attachment/" + id
				}
				if key != "" {
					refs[key] = append(refs[key], ResourceReference{File: name, Page: page, Object: object, Role: role})
				}
			}
			for _, attr := range node.attrs {
				if attr.Name.Space == "" && (editorResourceReferenceKind(attr.Name.Local, root.matchesOFD("Res")) == editorReferenceID || attr.Name.Local == "AttachID") {
					add(attr.Name.Local, attr.Value)
				}
			}
			if slices.Contains([]string{"Thumbnail", "Substitution"}, node.name.Local) {
				add(node.name.Local, strings.TrimSpace(editorImportText(file.data, node)))
			}
			for _, child := range node.children {
				if err := visit(child, object); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visit(root, ""); err != nil {
			return nil, err
		}
	}
	attachments, err := r.Attachments()
	if err != nil {
		return nil, err
	}
	for _, attachment := range attachments {
		item := ResourceInfo{Kind: "Attachment", ID: attachment.ID, Name: attachment.Name, Format: attachment.Format, File: r.ResPath(attachment.FileLoc), Complete: !scan.unsafe}
		if err := catalog.add(item, editorFontText{}); err != nil {
			return nil, err
		}
	}
	for i := range catalog.items {
		item := &catalog.items[i]
		key := item.ID
		if item.Kind == "Attachment" {
			key = "Attachment/" + item.ID
		}
		item.References = refs[key]
		if item.File != "" {
			if data, ok := r.files[cleanPackagePath(item.File)]; ok {
				item.Size = uint64(len(data))
			} else if file, ok := r.packageFile(item.File); ok {
				item.Size = file.UncompressedSize64
			} else {
				item.Missing = true
			}
		} else {
			item.Missing = item.Kind != "Font"
		}
	}
	return catalog, ctx.Err()
}

// add 加入资源声明，拒绝同一类型内的重复标识
// 入参: item 资源信息, node 声明位置
// 返回: error 标识冲突
func (c *resourceCatalog) add(item ResourceInfo, node editorFontText) error {
	key := item.Kind + "/" + item.ID
	if _, exists := c.nodes[key]; exists {
		return fmt.Errorf("duplicate resource %s", key)
	}
	if item.Kind != "Attachment" {
		for _, kind := range []string{"Font", "Image", "Audio", "Video"} {
			if _, exists := c.nodes[kind+"/"+item.ID]; exists {
				return fmt.Errorf("conflicting resource ID %s", item.ID)
			}
		}
	}
	c.nodes[key] = node
	c.items = append(c.items, item)
	return nil
}

// find 查找当前视图内的资源，不接受调用方提供的任意包路径
// 入参: kind 资源类型, id 资源标识
// 返回: ResourceInfo 资源信息, error 未找到资源
func (c *resourceCatalog) find(kind, id string) (ResourceInfo, error) {
	for _, item := range c.items {
		if item.Kind == kind && (item.ID == id || kind != "Attachment" && sameResourceID(item.ID, id)) {
			return item, nil
		}
	}
	return ResourceInfo{}, fmt.Errorf("resource not found: %s/%s", kind, id)
}
