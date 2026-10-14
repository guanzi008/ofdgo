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
	"io"
	"strings"
)

// 公文标引类型标识
const structureTypeOfficial = "od"

// 2013版电子公文标引命名空间
const structureNamespaceEGov2013 = "http://www.nits.org.cn/eGovDoc/2013"

// Structure 保存自定义标引入口及结构树，单个文件解析失败时通过Error报告，不影响其他标引
// CanBind表示已识别标引引用规则，包括公文标引及本库富文本标引，具体节点和目标仍由绑定接口校验
// Internal标记本库内部编辑标引，不删除或重排入口；界面跳过此项时必须保留原始数组索引
// 此标记不限制通用低级编辑接口，也不表示内部元数据内容已通过有效性校验
type Structure struct {
	TypeID    string         `json:"typeId,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	FileLoc   string         `json:"fileLoc"`
	CanBind   bool           `json:"canBind,omitempty"`
	Internal  bool           `json:"internal,omitempty"`
	Root      *StructureNode `json:"root,omitempty"`
	Error     string         `json:"error,omitempty"`
}

// StructureNode 保存标引节点的名称、属性、直接文本和有序子节点，不改变原始XML
// Text按文档顺序汇总直接文本并保留空白，不包含子节点文本
// References仅解释已识别公文标引或本库富文本标引中的ObjectRef；未知扩展保留结构，不推定其引用语义
type StructureNode struct {
	Name       string               `json:"name"`
	Namespace  string               `json:"namespace,omitempty"`
	Text       string               `json:"text,omitempty"`
	Attributes []StructureAttribute `json:"attributes,omitempty"`
	Children   []StructureNode      `json:"children,omitempty"`
	References []StructureReference `json:"references,omitempty"`
}

// StructureAttribute 保存展开命名空间后的标引属性，不包含命名空间声明
type StructureAttribute struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Value     string `json:"value"`
}

// StructureReference 记录同一页上按标引顺序排列的图元标识，不将页面标识当作页码
type StructureReference struct {
	PageID    string   `json:"pageId"`
	ObjectIDs []string `json:"objectIds"`
}

// StructureTarget 保存单个标引图元的原文和可见范围，Page从1开始，Bounds单位为毫米
// Error说明引用无法定位的原因；空Bounds表示图元存在但没有可见范围
type StructureTarget struct {
	Page   int    `json:"page"`
	PageID string `json:"pageId"`
	ID     string `json:"id"`
	Text   string `json:"text,omitempty"`
	Bounds Box    `json:"bounds"`
	Error  string `json:"error,omitempty"`
}

// Structure 读取当前文档及版本的自定义标引，不扫描页面或访问外部Schema
// 返回: []Structure 标引结构, error 清单读取错误
func (r *Reader) Structure() ([]Structure, error) {
	return r.StructureContext(context.Background())
}

// StructureContext 读取标引并响应取消，每次返回独立结构，未知命名空间不影响正文阅读
// 入参: ctx 取消上下文
// 返回: []Structure 标引结构, error 清单读取或取消错误
func (r *Reader) StructureContext(ctx context.Context) ([]Structure, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	tags, err := r.CustomTagsContext(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Structure, len(tags))
	for i, tag := range tags {
		item, err := r.readStructureContext(ctx, tag)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			item.Error = err.Error()
		}
		result[i] = item
	}
	return result, nil
}

// readStructureContext 读取单个标引文件，解析失败时保留入口信息及原始错误
// 入参: ctx 取消上下文, tag 标引入口
// 返回: Structure 标引结构, error 文件读取、解析或取消错误
func (r *Reader) readStructureContext(ctx context.Context, tag CustomTag) (Structure, error) {
	item := Structure{TypeID: tag.TypeID, Namespace: tag.NameSpace, FileLoc: tag.FileLoc}
	data, err := r.readFileViewContext(ctx, r.ResPath(tag.FileLoc))
	if err == nil {
		item.Root, err = parseStructure(ctx, data, tag)
	}
	if err != nil {
		return item, fmt.Errorf("read custom tag %s: %w", tag.FileLoc, err)
	}
	item.CanBind = structureOfficial(tag, item.Root.Namespace)
	item.Internal = tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace && item.Root.Namespace == richTextNamespace && item.Root.Name == "RichText"
	return item, nil
}

// parseStructure 解析有界XML结构，按GB/T33476.3的ObjectRef规则识别公文引用
// 入参: ctx 取消上下文, data 标引XML, tag 标引入口
// 返回: *StructureNode 根节点, error 解析、限额或取消错误
func parseStructure(ctx context.Context, data []byte, tag CustomTag) (*StructureNode, error) {
	d := xml.NewDecoder(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx})
	var root *StructureNode
	var stack []*StructureNode
	var text []*strings.Builder
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			count++
			if len(stack) >= 128 || count > 100000 {
				return nil, fmt.Errorf("custom tag structure limit exceeded")
			}
			if len(stack) == len(text) {
				text = append(text, new(strings.Builder))
			}
			node := StructureNode{Name: token.Name.Local, Namespace: token.Name.Space}
			for _, attr := range token.Attr {
				if attr.Name.Space != "xmlns" && !(attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					node.Attributes = append(node.Attributes, StructureAttribute{Name: attr.Name.Local, Namespace: attr.Name.Space, Value: attr.Value})
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("multiple custom tag roots")
				}
				root = &node
				stack = append(stack, root)
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, node)
				stack = append(stack, &parent.Children[len(parent.Children)-1])
			}
		case xml.CharData:
			if len(stack) > 0 {
				text[len(stack)-1].Write(token)
			} else if strings.Trim(string(token), " \t\r\n") != "" {
				return nil, fmt.Errorf("text outside custom tag root")
			}
		case xml.EndElement:
			node := stack[len(stack)-1]
			builder := text[len(stack)-1]
			node.Text = builder.String()
			builder.Reset()
			official := structureOfficial(tag, root.Namespace)
			for _, parent := range stack {
				official = official && parent.Namespace == root.Namespace
			}
			if official && node.Name == "ObjectRef" && len(node.Children) == 0 {
				var page string
				for _, attr := range node.Attributes {
					if attr.Name == "PageRef" && attr.Namespace == "" {
						page = attr.Value
					}
				}
				ids := structureObjectIDs(node.Text)
				if editorResourceID(page) == "" || len(ids) == 0 {
					return nil, fmt.Errorf("custom tag ObjectRef requires PageRef and object identifiers")
				}
				for _, id := range ids {
					if editorResourceID(id) == "" {
						return nil, fmt.Errorf("invalid custom tag object identifier: %s", id)
					}
				}
				node.References = []StructureReference{{PageID: page, ObjectIDs: ids}}
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty custom tag document")
	}
	return root, nil
}

// structureOfficial 识别公文和本库富文本标引的明确引用规则，不推断未知行业语义
// 入参: tag 标引入口, namespace 内容根节点命名空间
// 返回: bool 是否使用公文引用规则
func structureOfficial(tag CustomTag, namespace string) bool {
	if tag.TypeID == richTextTagType && tag.NameSpace == richTextNamespace && namespace == richTextNamespace {
		return true
	}
	if tag.TypeID == structureTypeOfficial {
		return true
	}
	if tag.NameSpace != namespace {
		return false
	}
	switch namespace {
	case structureNamespaceEGov2013:
		return true
	default:
		return false
	}
}

// structureObjectIDs 按公文标引规则拆分图元标识，保留顺序和重复项
// 入参: value ObjectRef文本
// 返回: []string 图元标识
func structureObjectIDs(value string) []string {
	return strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' })
}

// StructureTargets 按标引顺序定位指定节点及其后代引用，每个目标页面只遍历一次
// 入参: index 标引清单索引, path 相对根节点的子节点索引，均从0开始
// 返回: []StructureTarget 原文和页面范围，缺失引用单独报告, error 解析或渲染错误
func (r *Renderer) StructureTargets(index int, path []int) ([]StructureTarget, error) {
	return r.StructureTargetsContext(context.Background(), index, path)
}

// StructureTargetsContext 仅读取所选标引，在当前文档和版本内定位目标，不搜索其他文档的同名图元
// 入参: ctx 取消上下文, index 标引清单索引, path 子节点索引路径
// 返回: []StructureTarget 有序目标, error 解析、渲染或取消错误
func (r *Renderer) StructureTargetsContext(ctx context.Context, index int, path []int) ([]StructureTarget, error) {
	tags, err := r.Reader.CustomTagsContext(ctx)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(tags) {
		return nil, fmt.Errorf("custom tag structure not found: %d", index)
	}
	structure, err := r.Reader.readStructureContext(ctx, tags[index])
	if err != nil {
		return nil, err
	}
	node := structure.Root
	for _, i := range path {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if i < 0 || i >= len(node.Children) {
			return nil, fmt.Errorf("custom tag node not found")
		}
		node = &node.Children[i]
	}
	var references []StructureReference
	var collect func(*StructureNode) error
	collect = func(node *StructureNode) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		references = append(references, node.References...)
		for i := range node.Children {
			if err := collect(&node.Children[i]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(node); err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return nil, ctx.Err()
	}
	doc, err := r.Reader.DocContext(ctx)
	if err != nil {
		return nil, err
	}
	pages := signaturePageNumbers(doc)
	var result []StructureTarget
	for _, ref := range references {
		page, _ := resourceValue(pages, ref.PageID)
		for _, id := range ref.ObjectIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result = append(result, StructureTarget{Page: page, PageID: ref.PageID, ID: id})
		}
	}
	byPage := make(map[int]map[string]*StructureTarget)
	for i := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target := &result[i]
		if target.Page == 0 {
			target.Error = "custom tag page not found"
			continue
		}
		if byPage[target.Page] == nil {
			byPage[target.Page] = make(map[string]*StructureTarget)
		}
		byPage[target.Page][editorResourceID(target.ID)] = target
		target.Error = "custom tag object not found"
	}
	view := *r
	view.RenderAnnotations, view.pageContext = true, ctx
	for page, targets := range byPage {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := r.Reader.PageContentByIndexContext(ctx, page-1)
		if err != nil {
			return nil, err
		}
		visitor := &structureVisitor{ctx: ctx, renderer: &view, targets: targets}
		if err := view.WalkPage(content, visitor); err != nil {
			return nil, err
		}
	}
	for i, target := range result {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if source := byPage[target.Page][editorResourceID(target.ID)]; source != nil {
			result[i] = *source
		}
	}
	return result, nil
}
