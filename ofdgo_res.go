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
	"fmt"
	"strconv"
	"strings"
)

// Res 资源文件结构
type Res struct {
	XMLName               xml.Name              `xml:"Res"`
	BaseLoc               string                `xml:"BaseLoc,attr"`
	ColorSpaces           ColorSpaces           `xml:"ColorSpaces"`
	Fonts                 Fonts                 `xml:"Fonts"`
	MultiMedias           MultiMedias           `xml:"MultiMedias"`
	DrawParams            DrawParams            `xml:"DrawParams"`
	CompositeGraphicUnits CompositeGraphicUnits `xml:"CompositeGraphicUnits"`
}

// ColorSpaces 颜色空间集合
type ColorSpaces struct {
	ColorSpace []ColorSpace `xml:"ColorSpace"`
}

// ColorSpace 颜色空间定义
type ColorSpace struct {
	ID               string   `xml:"ID,attr"`
	Type             string   `xml:"Type,attr"`
	BitsPerComponent int      `xml:"BitsPerComponent,attr"`
	Profile          string   `xml:"Profile,attr,omitempty"`
	Palette          []string `xml:"Palette>CV"`
	profile          *colorProfile
}

// Fonts 字体集合
type Fonts struct {
	Font []Font `xml:"Font"`
}

// Font 字体定义
type Font struct {
	ID         string `xml:"ID,attr"`
	FontName   string `xml:"FontName,attr"`
	FamilyName string `xml:"FamilyName,attr"`
	Charset    string `xml:"Charset,attr"`
	Italic     bool   `xml:"Italic,attr"`
	Bold       bool   `xml:"Bold,attr"`
	Serif      bool   `xml:"Serif,attr"`
	FixedWidth bool   `xml:"FixedWidth,attr"`
	FontFile   string `xml:"FontFile"`
}

// MultiMedias 多媒体集合
type MultiMedias struct {
	MultiMedia []MultiMedia `xml:"MultiMedia"`
}

// MultiMedia 多媒体定义
type MultiMedia struct {
	ID        string `xml:"ID,attr"`
	Type      string `xml:"Type,attr"`
	Format    string `xml:"Format,attr"`
	MediaFile string `xml:"MediaFile"`
}

// DrawParams 绘制参数集合
type DrawParams struct {
	DrawParam []DrawParam `xml:"DrawParam"`
}

// DrawParam 绘制参数
// MiterLimitSet为true时，零截断值覆盖继承值，否则零值表示未指定
type DrawParam struct {
	ID             string       `xml:"ID,attr"`
	Relative       string       `xml:"Relative,attr"`
	ResourceID     string       `xml:"ResourceID,attr"`
	BaseLoc        string       `xml:"BaseLoc,attr"`
	Link           string       `xml:"Link,attr"`
	LineWidth      float64      `xml:"LineWidth,attr"`
	Join           string       `xml:"Join,attr"`
	Cap            string       `xml:"Cap,attr"`
	DashOffset     *float64     `xml:"DashOffset,attr"`
	DashPattern    string       `xml:"DashPattern,attr"`
	MiterLimit     float64      `xml:"MiterLimit,attr"`
	Font           string       `xml:"Font,attr"`
	Size           float64      `xml:"Size,attr"`
	Weight         int          `xml:"Weight,attr"`
	Italic         bool         `xml:"Italic,attr"`
	FillColor      *FillColor   `xml:"FillColor"`
	StrokeColor    *StrokeColor `xml:"StrokeColor"`
	LineWidthSet   bool         `xml:"-"`
	MiterLimitSet  bool         `xml:"-"`
	dashPatternSet bool
}

// CompositeGraphicUnits 复合图元集合
type CompositeGraphicUnits struct {
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
}

// CompositeGraphicUnit 复合图元
// Width、Height、Thumbnail和Substitution用于矢量资源定义
type CompositeGraphicUnit struct {
	ID                   string                 `xml:"ID,attr"`
	BaseLoc              string                 `xml:"BaseLoc,attr"`
	ResourceID           string                 `xml:"ResourceID,attr"`
	Boundary             string                 `xml:"Boundary,attr"`
	CTM                  string                 `xml:"CTM,attr"`
	DrawParam            string                 `xml:"DrawParam,attr"`
	Alpha                *int                   `xml:"Alpha,attr"`
	Visible              *bool                  `xml:"Visible,attr"`
	Width                float64                `xml:"Width,attr"`
	Height               float64                `xml:"Height,attr"`
	Thumbnail            string                 `xml:"Thumbnail"`
	Substitution         string                 `xml:"Substitution"`
	Objects              []GraphicObject        `xml:"-"`
	TextObject           []TextObject           `xml:"TextObject"`
	PathObject           []PathObject           `xml:"PathObject"`
	ImageObject          []ImageObject          `xml:"ImageObject"`
	CompositeGraphicUnit []CompositeGraphicUnit `xml:"CompositeGraphicUnit"`
	Clips                *Clips                 `xml:"Clips"`
	Actions              []Action               `xml:"Actions>Action"`
	states               map[string]editorCompositeState
	extentSet            bool
}

// UnmarshalXML 解析资源文件，隔离外部命名空间的同名集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (res *Res) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	if start.Name.Local != "Res" {
		return fmt.Errorf("expected Res element, got %s", start.Name.Local)
	}
	*res = Res{XMLName: start.Name, BaseLoc: attrValue(start, "BaseLoc")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		var target any
		switch node.Name.Local {
		case "ColorSpaces":
			target = &res.ColorSpaces
		case "Fonts":
			target = &res.Fonts
		case "MultiMedias":
			target = &res.MultiMedias
		case "DrawParams":
			target = &res.DrawParams
		case "CompositeGraphicUnits":
			target = &res.CompositeGraphicUnits
		default:
			return d.Skip()
		}
		return d.DecodeElement(target, &node)
	})
}

// UnmarshalXML 解析颜色空间集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (spaces *ColorSpaces) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "ColorSpace", &spaces.ColorSpace)
}

// UnmarshalXML 解析颜色空间属性及调色板。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (space *ColorSpace) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*space = ColorSpace{ID: attrValue(start, "ID"), Type: attrValue(start, "Type"), Profile: attrValue(start, "Profile")}
	if value := strings.TrimSpace(attrValue(start, "BitsPerComponent")); value != "" {
		bits, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		space.BitsPerComponent = bits
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "Palette" {
			return decodeOFDCollection(d, node, "CV", &space.Palette)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析字体集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (fonts *Fonts) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "Font", &fonts.Font)
}

// UnmarshalXML 解析字体声明及内嵌字体路径。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (font *Font) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*font = Font{ID: attrValue(start, "ID"), FontName: attrValue(start, "FontName"), FamilyName: attrValue(start, "FamilyName"), Charset: attrValue(start, "Charset")}
	for _, attr := range start.Attr {
		if attr.Name.Space != "" {
			continue
		}
		var target *bool
		switch attr.Name.Local {
		case "Italic":
			target = &font.Italic
		case "Bold":
			target = &font.Bold
		case "Serif":
			target = &font.Serif
		case "FixedWidth":
			target = &font.FixedWidth
		}
		if value := strings.TrimSpace(attr.Value); target != nil && value != "" {
			flag, err := strconv.ParseBool(value)
			if err != nil {
				return err
			}
			*target = flag
		}
	}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "FontFile" {
			return d.DecodeElement(&font.FontFile, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析多媒体集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (media *MultiMedias) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "MultiMedia", &media.MultiMedia)
}

// UnmarshalXML 解析多媒体声明及包内文件路径。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (media *MultiMedia) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	*media = MultiMedia{ID: attrValue(start, "ID"), Type: attrValue(start, "Type"), Format: attrValue(start, "Format")}
	return decodeOFDChildren(d, start, func(d *xml.Decoder, node xml.StartElement) error {
		if node.Name.Local == "MediaFile" {
			return d.DecodeElement(&media.MediaFile, &node)
		}
		return d.Skip()
	})
}

// UnmarshalXML 解析绘制参数集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (params *DrawParams) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "DrawParam", &params.DrawParam)
}

// UnmarshalXML 解析复合图元资源集合。
// 入参: d XML解码器, start 起始节点
// 返回: error 解码错误
func (units *CompositeGraphicUnits) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return decodeOFDCollection(d, start, "CompositeGraphicUnit", &units.CompositeGraphicUnit)
}

// sameResourceID 判断资源标识是否相同，数字标识按数值比较
// 入参: first 第一个标识, second 第二个标识
// 返回: bool 是否相同
func sameResourceID(first, second string) bool {
	if first == second {
		return true
	}
	id := editorResourceID(first)
	return id != "" && id == editorResourceID(second)
}

// resourceValue 优先查找原标识与标准十进制标识，回退别名有冲突时不猜测
// 入参: values 已加载资源, id 标识文本
// 返回: T 资源值, bool 是否存在唯一结果
func resourceValue[T comparable](values map[string]T, id string) (T, bool) {
	if value, ok := values[id]; ok {
		return value, true
	}
	var value T
	key := editorResourceID(id)
	if key == "" {
		return value, false
	}
	if value, ok := values[key]; ok {
		return value, true
	}
	found := false
	for alias, candidate := range values {
		if editorResourceID(alias) != key {
			continue
		}
		if found && value != candidate {
			var zero T
			return zero, false
		}
		value, found = candidate, true
	}
	return value, found
}
