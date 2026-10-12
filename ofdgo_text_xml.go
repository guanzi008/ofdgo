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
	"encoding/xml"
	"strconv"
	"strings"
)

// UnmarshalXML 解析字形变换，省略的字符数和字形数均取1
// 入参: decoder XML解码器, start 变换起始节点
// 返回: error 解码错误
func (transform *CGTransform) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	*transform = CGTransform{CodeCount: 1, GlyphCount: 1}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *int
		switch attr.Name.Local {
		case "CodePosition":
			target = &transform.CodePosition
		case "CodeCount":
			target = &transform.CodeCount
		case "GlyphCount":
			target = &transform.GlyphCount
		}
		if target != nil {
			value, err := strconv.Atoi(strings.TrimSpace(attr.Value))
			if err != nil {
				return err
			}
			*target = value
		}
	}
	return decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Glyphs" {
			return d.DecodeElement(&transform.Glyphs, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析文字定位及原文，不使用带外部命名空间的定位属性
// 入参: decoder XML解码器, start 文字节点
// 返回: error 解码错误
func (code *TextCode) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type plain TextCode
	var value plain
	start = ofdXMLStart(start)
	if err := decoder.DecodeElement(&value, &start); err != nil {
		return err
	}
	*code = TextCode(value)
	return nil
}

// UnmarshalXML 解析标准文字节点，将各TextCode前的局部字形位置统一为对象内索引
// 入参: decoder XML解码器, start 对象起始节点
// 返回: error 解码错误
func (obj *TextObject) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	type plain TextObject
	var value plain
	if err := decodeGraphicAttributes(start, &value); err != nil {
		return err
	}
	offset, pending := 0, 0
	if err := decodeOFDChildren(decoder, start, func(d *xml.Decoder, node xml.StartElement) error {
		switch node.Name.Local {
		case "FillColor":
			return d.DecodeElement(&value.FillColor, &node)
		case "StrokeColor":
			return d.DecodeElement(&value.StrokeColor, &node)
		case "Clips":
			return d.DecodeElement(&value.Clips, &node)
		case "Actions":
			return decodeOFDActions(d, node, &value.Actions)
		case "CGTransform":
			return d.DecodeElement(&value.CGTransform, &node)
		case "TextCode":
			var code TextCode
			if err := d.DecodeElement(&code, &node); err != nil {
				return err
			}
			for i := pending; i < len(value.CGTransform); i++ {
				value.CGTransform[i].CodePosition += offset
			}
			pending = len(value.CGTransform)
			offset += len(textCodeRunes(code.Value))
			value.TextCode = append(value.TextCode, code)
			return nil
		}
		return d.Skip()
	}); err != nil {
		return err
	}
	*obj = TextObject(value)
	for _, attr := range ofdXMLStart(start).Attr {
		if attr.Name.Local == "LineWidth" {
			obj.LineWidthSet = obj.LineWidth == 0
		}
		if attr.Name.Local == "MiterLimit" {
			obj.MiterLimitSet = obj.MiterLimit == 0
		}
		if attr.Name.Local == "DashPattern" {
			obj.dashPatternSet = obj.DashPattern == ""
		}
	}
	return nil
}
