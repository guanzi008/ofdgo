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
	"cmp"
	"context"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// ResourceInfo 描述当前文档及所选版本的资源，Size为文件字节数
// Kind为Font、Image、Audio、Video、Attachment、ColorSpace或DrawParam
// Complete表示已完整识别引用；不包含其他文档和历史版本的引用
// ColorSpace的File为ICC文件，未声明ICC及内联DrawParam的File为空且Missing为false
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

// ResourceReference 记录资源引用所在的XML及字段，Page为零基页码，非页面或多实例文件为-1
// Object为最近的图元标识，模板、注解、图案和复合资源通过File区分，不推断其页面实例数
type ResourceReference struct {
	File   string `json:"file"`
	Page   int    `json:"page"`
	Object string `json:"object"`
	Role   string `json:"role"`
}

// resourceCatalog 保存当前资源声明和原文位置，供查询及隔离修改复用
type resourceCatalog struct {
	scan   *editorFontDocument
	items  []ResourceInfo
	nodes  map[string]editorFontText
	images []editorFontText
}

// Resources 列出当前文档及所选版本的资源，标记缺失文件，不读取二进制内容或执行媒体动作
// 重复查询复用已完成的扫描，返回值及引用位置与阅读器相互独立
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 资源及引用位置, error 结构或读取错误
func (r *Reader) Resources(ctx context.Context) ([]ResourceInfo, error) {
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	result := slices.Clone(items)
	for i := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result[i].References = slices.Clone(result[i].References)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// OpenResource 打开当前资源的原始文件，ColorSpace导出ICC，无ICC或内联DrawParam不提供文件流
// 外部字体同样无可导出的内嵌文件，定义读取使用ColorSpace或DrawParam
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: io.ReadCloser 文件流，由调用方关闭, error 资源或文件错误
func (r *Reader) OpenResource(ctx context.Context, kind, id string) (io.ReadCloser, error) {
	items, err := r.resourceList(ctx)
	if err != nil {
		return nil, err
	}
	item, err := findResource(items, kind, id)
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

// resourceList 缓存当前阅读视图的资源摘要，不保留扫描使用的XML树
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 内部只读摘要, error 扫描或取消错误
func (r *Reader) resourceList(ctx context.Context) ([]ResourceInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.resourceInfoCache != nil {
		return r.resourceInfoCache, nil
	}
	catalog, err := r.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	r.resourceInfoCache = catalog.items
	return r.resourceInfoCache, nil
}

// resourceCatalog 单次解析标准内容路径，复用字体扫描的XML树收集资源声明及引用
// 结果按文件名及原节点顺序归集，不把任意数字属性作为资源引用
// 入参: ctx 取消上下文
// 返回: *resourceCatalog 资源索引, error 结构或读取错误
func (r *Reader) resourceCatalog(ctx context.Context) (*resourceCatalog, error) {
	catalog := &resourceCatalog{nodes: make(map[string]editorFontText), items: []ResourceInfo{}}
	pages := make(map[string]int)
	doc, err := r.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	for i, page := range doc.Pages.Page {
		name := cleanPackagePath(r.ResPath(page.BaseLoc))
		if _, exists := pages[name]; exists {
			pages[name] = -1
		} else {
			pages[name] = i
		}
	}
	for _, page := range doc.CommonData.TemplatePage {
		pages[cleanPackagePath(r.ResPath(page.BaseLoc))] = -1
	}
	refs := make(map[string][]ResourceReference)
	scan, err := scanEditorFontFiles(ctx, r, r.DocumentIndex(), func(file *editorFontXML, root *editorXML) error {
		name := file.name
		if root.matchesOFD("Res") {
			for _, group := range root.children {
				for _, node := range group.children {
					item := ResourceInfo{ID: editorResourceID(node.attr("ID"))}
					field := ""
					if group.matchesOFD("Fonts") && node.matchesOFD("Font") {
						item.Kind, item.Name, field = "Font", node.attr("FontName"), "FontFile"
					} else if group.matchesOFD("MultiMedias") && node.matchesOFD("MultiMedia") && slices.Contains([]string{"Image", "Audio", "Video"}, node.attr("Type")) {
						item.Kind, item.Format, field = node.attr("Type"), node.attr("Format"), "MediaFile"
					} else if group.matchesOFD("ColorSpaces") && node.matchesOFD("ColorSpace") {
						item.Kind, item.Name = "ColorSpace", node.attr("Type")
						if profile := strings.TrimSpace(node.attr("Profile")); profile != "" {
							item.File = resolveResourcePath(name, root.attr("BaseLoc"), profile)
							item.Format = "ICC"
						}
					} else if group.matchesOFD("DrawParams") && node.matchesOFD("DrawParam") {
						item.Kind, item.Name = "DrawParam", "DrawParam "+item.ID
					} else {
						continue
					}
					if item.ID == "" {
						return fmt.Errorf("invalid resource ID")
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
						return err
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
			if node.matchesOFD("ImageObject") {
				catalog.images = append(catalog.images, editorFontText{file: file, node: node})
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
			if slices.Contains([]string{"Thumbnail", "Substitution", "DefaultCS"}, node.name.Local) {
				add(node.name.Local, strings.TrimSpace(editorImportText(file.data, node)))
			}
			for _, child := range node.children {
				if err := visit(child, object); err != nil {
					return err
				}
			}
			return nil
		}
		return visit(root, "")
	})
	if err != nil {
		return nil, err
	}
	catalog.scan = scan
	slices.SortStableFunc(catalog.items, func(a, b ResourceInfo) int {
		return cmp.Compare(catalog.nodes[a.Kind+"/"+a.ID].file.name, catalog.nodes[b.Kind+"/"+b.ID].file.name)
	})
	slices.SortStableFunc(catalog.images, func(a, b editorFontText) int {
		return cmp.Compare(a.file.name, b.file.name)
	})
	attachments, err := r.attachmentsContext(ctx, true)
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
		item.Complete = !scan.unsafe
		key := item.ID
		if item.Kind == "Attachment" {
			key = "Attachment/" + item.ID
		}
		item.References = refs[key]
		slices.SortStableFunc(item.References, func(a, b ResourceReference) int {
			return cmp.Compare(a.File, b.File)
		})
		if item.File != "" {
			if data, ok := r.files[cleanPackagePath(item.File)]; ok {
				item.Size = uint64(len(data))
			} else if file, ok := r.packageFile(item.File); ok {
				item.Size = file.UncompressedSize64
			} else {
				item.Missing = true
			}
		} else {
			item.Missing = item.Kind != "Font" && item.Kind != "ColorSpace" && item.Kind != "DrawParam"
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
		for _, kind := range []string{"Font", "Image", "Audio", "Video", "ColorSpace", "DrawParam"} {
			if _, exists := c.nodes[kind+"/"+item.ID]; exists {
				return fmt.Errorf("conflicting resource ID %s", item.ID)
			}
		}
	}
	c.nodes[key] = node
	c.items = append(c.items, item)
	return nil
}

// findResource 查找当前视图内的资源，不接受调用方提供的任意包路径
// 入参: items 资源列表, kind 资源类型, id 资源标识
// 返回: ResourceInfo 资源信息, error 未找到资源
func findResource(items []ResourceInfo, kind, id string) (ResourceInfo, error) {
	for _, item := range items {
		if item.Kind == kind && (item.ID == id || kind != "Attachment" && sameResourceID(item.ID, id)) {
			return item, nil
		}
	}
	return ResourceInfo{}, fmt.Errorf("resource not found: %s/%s", kind, id)
}
