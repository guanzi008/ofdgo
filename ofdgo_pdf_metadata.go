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
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/xiaoqidun/pdfgo"
)

// PDF元数据使用的RDF、Dublin Core及Adobe命名空间
const (
	pdfMetadataRDF = "http://www.w3.org/1999/02/22-rdf-syntax-ns#"
	pdfMetadataDC  = "http://purl.org/dc/elements/1.1/"
	pdfMetadataXMP = "http://ns.adobe.com/xap/1.0/"
	pdfMetadataPDF = "http://ns.adobe.com/pdf/1.3/"
	pdfMetadataXML = "adobe:ns:meta/"
)

// PDF来源元数据的标引定义
const (
	pdfMetadataTagType      = "OFDGo.PDFMetadata"
	pdfInformationTagType   = "OFDGo.PDFInformation"
	pdfInformationNamespace = "https://ofdgo.aite.me/ns/pdf/information/1"
)

// pdfMetadataValue 保留字段是否声明及其来源，空文本不作为缺省值
type pdfMetadataValue struct {
	text string
	xmp  bool
}

// documentMetadata 保留PDF元数据原文并映射文档描述，来源软件与扩展字段保存在独立标引中
// 入参: ctx 取消上下文
// 返回: error 读取、解析、日期或提交错误
func (p *pdfImporter) documentMetadata(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := p.reader.Info()
	if err != nil {
		return err
	}
	values, original, err := p.informationMetadata(ctx, info)
	if err != nil {
		return err
	}
	stream, err := p.reader.Metadata()
	if err != nil {
		return err
	}
	var packet []byte
	var root *StructureNode
	if stream != nil {
		packet, err = stream.DecodeContext(ctx)
		if err != nil {
			return err
		}
		root, err = parseStructure(ctx, packet, CustomTag{})
		if err != nil {
			return err
		}
		xmp, err := pdfMetadataFields(ctx, root)
		if err != nil {
			return err
		}
		newer := false
		if stamp, ok := xmp["MetadataDate"]; ok && stamp.text != "" && info.ModDate != "" {
			date, _, zoned, err := pdfMetadataDate(stamp)
			if err != nil {
				return err
			}
			modified, err := pdfgo.ParseDate(info.ModDate)
			if err != nil {
				return err
			}
			newer = zoned && modified.After(date)
		}
		for key, value := range xmp {
			if _, exists := values[key]; !newer || !exists {
				values[key] = value
			}
		}
	}
	projected := p.editor.Info
	for _, field := range []struct {
		name string
		text *string
	}{{"Title", &projected.Title}, {"Author", &projected.Author}, {"Subject", &projected.Subject}} {
		if value, ok := values[field.name]; ok && structureXMLText(value.text) {
			*field.text = value.text
		}
	}
	if value, ok := values["Keywords"]; ok && structureXMLText(value.text) {
		projected.Keywords = Keywords{value.text}
	}
	for _, field := range []struct {
		name string
		text *string
	}{{"CreationDate", &projected.CreationDate}, {"ModDate", &projected.ModDate}} {
		if value, ok := values[field.name]; ok {
			date, day, _, err := pdfMetadataDate(value)
			if err != nil {
				return fmt.Errorf("PDF metadata %s: %w", field.name, err)
			}
			*field.text = ""
			if day {
				*field.text = date.Format("2006-01-02")
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.editor.SetInfo(projected)
	if root != nil {
		if _, err := p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfMetadataTagType, NameSpace: root.Namespace}, packet); err != nil {
			return err
		}
	}
	if len(original.Children) != 0 {
		data, err := encodeStructureNode(original)
		if err != nil {
			return err
		}
		_, err = p.editor.addStructureXML(ctx, CustomTag{TypeID: pdfInformationTagType, NameSpace: pdfInformationNamespace}, data)
		return err
	}
	return ctx.Err()
}

// informationMetadata 收集已声明字段并保存原始文本，XML无法表示的字符使用Base64保留
// 入参: ctx 取消上下文, info 已解码的信息字典
// 返回: map[string]pdfMetadataValue 可映射字段, StructureNode 原始字段, error 读取或取消错误
func (p *pdfImporter) informationMetadata(ctx context.Context, info pdfgo.DocumentInfo) (map[string]pdfMetadataValue, StructureNode, error) {
	root := StructureNode{Name: "Information", Namespace: pdfInformationNamespace}
	values := make(map[string]pdfMetadataValue)
	object, err := p.reader.Resolve(p.reader.Trailer["Info"])
	if err != nil || object == nil {
		return values, root, err
	}
	dict, ok := object.(pdfgo.Dictionary)
	if !ok {
		return nil, root, fmt.Errorf("invalid PDF information dictionary")
	}
	known := map[pdfgo.Name]string{
		"Title": info.Title, "Author": info.Author, "Subject": info.Subject, "Keywords": info.Keywords,
		"Creator": info.Creator, "Producer": info.Producer, "CreationDate": info.CreationDate, "ModDate": info.ModDate,
		"Trapped": string(info.Trapped),
	}
	for _, key := range slices.Sorted(maps.Keys(dict)) {
		if err := ctx.Err(); err != nil {
			return nil, root, err
		}
		value, err := p.reader.Resolve(dict[key])
		if err != nil {
			return nil, root, err
		}
		if value == nil {
			continue
		}
		text, standard := known[key]
		if !standard {
			text = info.Custom[key]
		} else {
			values[string(key)] = pdfMetadataValue{text: text}
		}
		name := string(key)
		field := StructureNode{Name: "Field", Namespace: pdfInformationNamespace}
		field.Attributes = append(field.Attributes, StructureAttribute{Name: "Name", Value: name})
		if !structureXMLText(name) {
			field.Attributes[0].Value = base64.StdEncoding.EncodeToString([]byte(name))
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "NameEncoding", Value: "base64"})
		}
		if key == "Trapped" {
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "Type", Value: "name"})
		}
		if !structureXMLText(text) {
			text = base64.StdEncoding.EncodeToString([]byte(text))
			field.Attributes = append(field.Attributes, StructureAttribute{Name: "Encoding", Value: "base64"})
		}
		field.Text = text
		root.Children = append(root.Children, field)
	}
	return values, root, nil
}

// pdfMetadataFields 读取文档级RDF描述，日期及PDF关键词不与其他同名扩展混用
// 入参: ctx 取消上下文, root 元数据根节点
// 返回: map[string]pdfMetadataValue 已声明的对应字段, error 取消错误
func pdfMetadataFields(ctx context.Context, root *StructureNode) (map[string]pdfMetadataValue, error) {
	result := make(map[string]pdfMetadataValue)
	var rdf *StructureNode
	if root.Namespace == pdfMetadataRDF && root.Name == "RDF" {
		rdf = root
	} else if root.Namespace == pdfMetadataXML && (root.Name == "xmpmeta" || root.Name == "xapmeta") {
		for i := range root.Children {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child := &root.Children[i]
			if child.Namespace == pdfMetadataRDF && child.Name == "RDF" {
				rdf = child
				break
			}
		}
	}
	if rdf == nil {
		return result, ctx.Err()
	}
	fields := map[xml.Name]string{
		{Space: pdfMetadataDC, Local: "title"}: "Title", {Space: pdfMetadataDC, Local: "creator"}: "Author",
		{Space: pdfMetadataDC, Local: "description"}: "Subject", {Space: pdfMetadataPDF, Local: "Keywords"}: "Keywords",
		{Space: pdfMetadataXMP, Local: "CreateDate"}: "CreationDate", {Space: pdfMetadataXMP, Local: "ModifyDate"}: "ModDate",
		{Space: pdfMetadataXMP, Local: "MetadataDate"}: "MetadataDate",
	}
	for _, description := range rdf.Children {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if description.Namespace != pdfMetadataRDF || description.Name != "Description" {
			continue
		}
		for _, attr := range description.Attributes {
			if key, ok := fields[xml.Name{Space: attr.Namespace, Local: attr.Name}]; ok && attr.Namespace != pdfMetadataDC {
				result[key] = pdfMetadataValue{text: attr.Value, xmp: true}
			}
		}
		for _, property := range description.Children {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key, ok := fields[xml.Name{Space: property.Namespace, Local: property.Name}]
			if !ok {
				continue
			}
			arrayValue := property.Namespace == pdfMetadataDC
			property = pdfMetadataProperty(property)
			if arrayValue {
				kind := "Alt"
				if key == "Author" {
					kind = "Seq"
				}
				found := false
				for _, array := range property.Children {
					if array.Namespace == pdfMetadataRDF && array.Name == kind && len(array.Children) != 0 {
						item := array.Children[0]
						if item.Namespace == pdfMetadataRDF && item.Name == "li" {
							property = item
							found = true
						}
						break
					}
				}
				if !found {
					continue
				}
			}
			if text, ok := pdfMetadataText(property); ok {
				result[key] = pdfMetadataValue{text: text, xmp: true}
			}
		}
	}
	return result, ctx.Err()
}

// pdfMetadataProperty 提取带限定符的rdf:value，保留数组及结构边界
// 入参: node 属性节点
// 返回: StructureNode 属性值节点
func pdfMetadataProperty(node StructureNode) StructureNode {
	for {
		if len(node.Children) == 1 && node.Children[0].Namespace == pdfMetadataRDF && node.Children[0].Name == "Description" {
			node = node.Children[0]
			continue
		}
		for _, attr := range node.Attributes {
			if attr.Namespace == pdfMetadataRDF && attr.Name == "value" {
				return StructureNode{Text: attr.Value}
			}
		}
		found := false
		for _, child := range node.Children {
			if child.Namespace == pdfMetadataRDF && child.Name == "value" {
				node, found = child, true
				break
			}
		}
		if !found {
			return node
		}
	}
}

// pdfMetadataText 读取简单值，不拼接结构或数组文本，也不将资源URI当作文本
// 入参: node 属性节点
// 返回: string 文本, bool 是否为支持的简单值
func pdfMetadataText(node StructureNode) (string, bool) {
	node = pdfMetadataProperty(node)
	for _, attr := range node.Attributes {
		if attr.Namespace == pdfMetadataRDF && (attr.Name == "resource" || attr.Name == "nodeID" || attr.Name == "parseType") {
			return "", false
		}
	}
	return node.Text, len(node.Children) == 0
}

// pdfMetadataDate 保留当地日历日期，PDF省略时区按GMT，XMP省略日或时区时不推算
// 入参: value 来源日期
// 返回: time.Time 解析值, bool 是否精确到日, bool 是否可比较绝对时间, error 日期错误
func pdfMetadataDate(value pdfMetadataValue) (time.Time, bool, bool, error) {
	if value.text == "" {
		return time.Time{}, false, false, nil
	}
	if !value.xmp {
		date, err := pdfgo.ParseDate(value.text)
		return date, err == nil && date.Year() > 0, err == nil, err
	}
	if !strings.Contains(value.text, ",") {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02", "2006-01", "2006"} {
			if date, err := time.Parse(layout, value.text); err == nil {
				zoned := strings.Contains(layout, "Z07:00")
				if zoned && !strings.HasSuffix(value.text, "Z") {
					zone := value.text[len(value.text)-6:]
					if zone[1:3] > "23" || zone[4:] > "59" {
						return time.Time{}, false, false, fmt.Errorf("invalid XMP time zone")
					}
				}
				return date, len(value.text) >= 10 && date.Year() > 0, zoned, nil
			}
		}
	}
	return time.Time{}, false, false, fmt.Errorf("invalid XMP date")
}
