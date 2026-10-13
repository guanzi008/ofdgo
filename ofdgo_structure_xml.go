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
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// encodeStructureNode 编码有界标引子树，引用由ObjectRef节点表达，不重复编码派生References字段
// 入参: node 根节点
// 返回: []byte XML片段, error 名称、属性或编码错误
func encodeStructureNode(node StructureNode) ([]byte, error) {
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	count := 0
	var write func(StructureNode, int) error
	write = func(node StructureNode, depth int) error {
		count++
		if depth > 128 || count > 100000 {
			return fmt.Errorf("custom tag structure limit exceeded")
		}
		if err := validateStructureNode(node); err != nil {
			return err
		}
		start := xml.StartElement{Name: xml.Name{Space: node.Namespace, Local: node.Name}}
		switch node.Namespace {
		case "":
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "xmlns"}, Value: ""})
		case "http://www.w3.org/XML/1998/namespace":
			start.Name = xml.Name{Local: "xml:" + node.Name}
		}
		for _, attr := range node.Attributes {
			start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Space: attr.Namespace, Local: attr.Name}, Value: attr.Value})
		}
		if err := encoder.EncodeToken(start); err != nil {
			return err
		}
		if err := encoder.EncodeToken(xml.CharData(node.Text)); err != nil {
			return err
		}
		for _, child := range node.Children {
			if err := write(child, depth+1); err != nil {
				return err
			}
		}
		return encoder.EncodeToken(start.End())
	}
	if err := write(node, 1); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// validateStructureNode 校验XML名称和文本，禁止重复属性及伪造命名空间声明
// 入参: node 节点字段
// 返回: error 字段校验错误
func validateStructureNode(node StructureNode) error {
	if !ofdXMLIDValid(node.Name) || node.Name == "xmlns" {
		return fmt.Errorf("invalid custom tag node name: %s", node.Name)
	}
	if !structureXMLText(node.Text) || !structureXMLText(node.Namespace) || node.Namespace == "http://www.w3.org/2000/xmlns/" {
		return fmt.Errorf("invalid custom tag text or namespace")
	}
	seen := make(map[xml.Name]bool)
	for _, attr := range node.Attributes {
		name := xml.Name{Space: attr.Namespace, Local: attr.Name}
		if !ofdXMLIDValid(attr.Name) || attr.Name == "xmlns" || attr.Namespace == "http://www.w3.org/2000/xmlns/" || !structureXMLText(attr.Namespace) || !structureXMLText(attr.Value) || seen[name] {
			return fmt.Errorf("invalid or duplicate custom tag attribute: %s", attr.Name)
		}
		seen[name] = true
	}
	return nil
}

// structureXMLText 检查XML 1.0允许的字符，不以替换字符掩盖无效输入
// 入参: value 文本
// 返回: bool 是否可无损编码
func structureXMLText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r != '\t' && r != '\n' && r != '\r' && (r < 0x20 || r == 0xfffe || r == 0xffff) {
			return false
		}
	}
	return true
}

// updateStructureNodeXML 替换节点字段，保留子树继承的命名空间和非文本XML内容
// 入参: data 原XML, source 原节点, node 新字段
// 返回: []byte 独立节点片段, error 校验或编码错误
func updateStructureNodeXML(data []byte, source *editorXML, node StructureNode) ([]byte, error) {
	if err := validateStructureNode(node); err != nil {
		return nil, err
	}
	fragment, err := editorXMLStandalone(data[source.start:source.end], source)
	if err != nil {
		return nil, err
	}
	before, err := parseStructure(context.Background(), fragment, CustomTag{})
	if err != nil {
		return nil, err
	}
	if node.Name == before.Name && node.Namespace == before.Namespace && node.Text == before.Text && slices.Equal(node.Attributes, before.Attributes) {
		return data[source.start:source.end], nil
	}
	root, err := parseEditorXML(fragment)
	if err != nil {
		return nil, err
	}
	if before.Text != node.Text {
		var text bytes.Buffer
		if err := xml.EscapeText(&text, []byte(node.Text)); err != nil {
			return nil, err
		}
		fragment, err = editorXMLDirectText(fragment, root, text.Bytes())
		if err != nil {
			return nil, err
		}
	}
	root, err = parseEditorXML(fragment)
	if err != nil {
		return nil, err
	}
	var attrs []xml.Attr
	used := make(map[string]bool)
	for _, attr := range root.attrs {
		if attr.Name.Space == "xmlns" {
			attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns:" + attr.Name.Local}, Value: attr.Value})
			used[attr.Name.Local] = true
		} else if attr.Name.Space == "" && attr.Name.Local == "xmlns" {
			attrs = append(attrs, attr)
		}
	}
	qualify := func(name, namespace string) string {
		if namespace == "" {
			return name
		}
		if namespace == "http://www.w3.org/XML/1998/namespace" {
			return "xml:" + name
		}
		for _, attr := range attrs {
			if strings.HasPrefix(attr.Name.Local, "xmlns:") && attr.Value == namespace {
				return strings.TrimPrefix(attr.Name.Local, "xmlns:") + ":" + name
			}
		}
		prefix := "tag"
		for i := 1; used[prefix]; i++ {
			prefix = "tag" + strconv.Itoa(i)
		}
		used[prefix] = true
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: "xmlns:" + prefix}, Value: namespace})
		return prefix + ":" + name
	}
	name := qualify(node.Name, node.Namespace)
	if node.Namespace == "" {
		for i := range attrs {
			if attrs[i].Name.Local == "xmlns" {
				attrs[i].Value = ""
			}
		}
	}
	for _, attr := range node.Attributes {
		key := qualify(attr.Name, attr.Namespace)
		attrs = append(attrs, xml.Attr{Name: xml.Name{Local: key}, Value: attr.Value})
	}
	var output bytes.Buffer
	encoder := xml.NewEncoder(&output)
	start := xml.StartElement{Name: xml.Name{Local: name}, Attr: attrs}
	if err := encoder.EncodeToken(start); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	if root.open != root.end {
		if node.Namespace == "" && before.Namespace != "" {
			var patches []editorXMLPatch
			for _, child := range root.children {
				standalone, err := editorXMLStandalone(fragment[child.start:child.end], child)
				if err != nil {
					return nil, err
				}
				patches = append(patches, editorXMLPatch{child.start - root.open, child.end - root.open, standalone})
			}
			output.Write(editorPatchXML(fragment[root.open:root.close], patches))
		} else {
			output.Write(fragment[root.open:root.close])
		}
	}
	if err := encoder.EncodeToken(start.End()); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
