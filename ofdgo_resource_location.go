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
	"maps"
	"slices"
	"strings"
)

// ResourceLocation 保存资源引用的页面实例范围，Page从1开始，Bounds单位为毫米
// Object为承载引用的图元标识；隐藏或完全裁剪的图元不产生定位结果
type ResourceLocation struct {
	Page   int    `json:"page"`
	Object string `json:"object"`
	Bounds Box    `json:"bounds"`
}

// ResourceLocations 定位当前文档及版本内的一处资源引用，返回各页面上的可见使用范围
// 模板、图案和复合资源沿引用关系定位承载图元，不将资源声明位置当作页面位置
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识, reference 为Resources返回的引用索引
// 返回: []ResourceLocation 页面位置，无可见图元时为空, error 引用、度量或取消错误
func (r *Renderer) ResourceLocations(ctx context.Context, kind, id string, reference int) ([]ResourceLocation, error) {
	catalog, err := r.Reader.resourceCatalog(ctx)
	if err != nil {
		return nil, err
	}
	item, err := findResource(catalog.items, kind, id)
	if err != nil {
		return nil, err
	}
	if reference < 0 || reference >= len(item.References) {
		return nil, fmt.Errorf("resource reference not found")
	}
	ref := item.References[reference]
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool)
	users := make(map[string][]string)
	for _, file := range catalog.scan.files {
		root, err := parseEditorXMLContext(ctx, file.data)
		if err != nil {
			return nil, err
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
			owner := resourceReferenceOwner(node)
			if owner != "" && resourceColorNode(node) && node.attr("ColorSpace") == "" && doc.CommonData.DefaultCS != 0 {
				dependency := fmt.Sprint(doc.CommonData.DefaultCS)
				users[dependency] = append(users[dependency], owner)
				if ref.Role == "DefaultCS" && sameResourceID(dependency, item.ID) {
					wanted[owner] = true
				}
			}
			add := func(role, value string) {
				if owner == "" {
					return
				}
				if role != "AttachID" {
					if dependency := editorResourceID(value); dependency != "" && dependency != owner {
						users[dependency] = append(users[dependency], owner)
					}
				}
				match := value == item.ID
				if kind != "Attachment" {
					match = sameResourceID(value, item.ID)
				}
				if file.name == ref.File && object == ref.Object && role == ref.Role && match {
					wanted[owner] = true
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
			parent := node.parent
			for parent != nil && parent.matchesOFD("PageBlock") {
				parent = parent.parent
			}
			if owner != "" && parent != nil && parent.matchesOFD("Layer") {
				if layer := editorResourceID(parent.attr("ID")); layer != "" && layer != owner {
					users[layer] = append(users[layer], owner)
				}
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
	queue := slices.Collect(maps.Keys(wanted))
	for len(queue) != 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := queue[0]
		queue = queue[1:]
		for _, user := range users[id] {
			if !wanted[user] {
				wanted[user] = true
				queue = append(queue, user)
			}
		}
	}
	view := *r
	view.RenderAnnotations, view.pageContext = true, ctx
	var result []ResourceLocation
	for page := range doc.Pages.Page {
		if ref.Page >= 0 && ref.Page != page || len(wanted) == 0 {
			continue
		}
		content, err := r.Reader.PageContentByIndexContext(ctx, page)
		if err != nil {
			return nil, err
		}
		targets := make(map[string]*StructureTarget, len(wanted))
		for id := range wanted {
			targets[id] = &StructureTarget{ID: id}
		}
		visitor := &structureVisitor{ctx: ctx, renderer: &view, targets: targets, boundsOnly: true}
		if err := view.WalkPage(content, visitor); err != nil {
			return nil, err
		}
		for _, id := range slices.Sorted(maps.Keys(targets)) {
			box := targets[id].Bounds
			if box.W > 0 && box.H > 0 {
				result = append(result, ResourceLocation{Page: page + 1, Object: id, Bounds: box})
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// resourceReferenceOwner 查找承载引用的资源定义或最外层图元，图层只承接其自身的绘制参数
// 入参: node 引用节点
// 返回: string 图元或资源标识，无法映射时为空
func resourceReferenceOwner(node *editorXML) string {
	id := ""
	for current := node; current != nil; current = current.parent {
		if !current.matchesOFD(current.name.Local) {
			return ""
		}
		if slices.Contains([]string{"TextObject", "PathObject", "ImageObject", "CompositeObject", "CompositeGraphicUnit"}, current.name.Local) || current.parent != nil && current.name.Local == editorResourceElement(current.parent.name.Local) {
			id = editorResourceID(current.attr("ID"))
		}
		if id == "" && current.matchesOFD("Layer") {
			id = editorResourceID(current.attr("ID"))
		}
	}
	return id
}
