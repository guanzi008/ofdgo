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
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"html"
	"image"
	"io"
	"math"
	"strings"

	"github.com/tdewolff/canvas"
	canvasimage "github.com/tdewolff/canvas/image"
	"github.com/tdewolff/canvas/renderers/svg"
	basefont "github.com/xiaoqidun/ofdgo/internal/base/font"
	"github.com/xiaoqidun/pdfgo"
)

// svgCreatorMetadata 记录SVG制作软件，不参与页面绘制
const svgCreatorMetadata = `<metadata>` + creatorXMP + `</metadata>`

// svgResourceRenderer 分离外部资源的SVG渲染器
type svgResourceRenderer struct {
	*svg.SVG
	renderer      *Renderer
	writer        io.Writer
	fonts         []SVGFont
	images        []SVGImage
	imageNames    map[svgImageKey]string
	seen          map[string]bool
	styles        strings.Builder
	err           error
	objects       map[*GraphicObject]string
	objectStack   []svgObjectGroup
	embeddedFonts bool
}

// svgImageKey 按原图及输出精度复用图片编码，避免不同显示尺寸共用低清结果
type svgImageKey struct {
	image image.Image
	size  image.Point
}

// svgObjectGroup 记录编辑分组的稳定路径，不把底纹内部绘制当作成员
type svgObjectGroup struct {
	path      string
	next      int
	composite bool
}

// RenderImage 按输出策略编码图片，分离资源时保持尺寸和变换
// 入参: img 图片对象, m 变换矩阵
func (s *svgResourceRenderer) RenderImage(img image.Image, m canvas.Matrix) {
	if s.err != nil {
		return
	}
	ctx := context.Background()
	if s.renderer != nil {
		ctx = s.renderer.outputContext()
	}
	if s.err = ctx.Err(); s.err != nil {
		return
	}
	var target image.Point
	if s.renderer != nil && s.renderer.Compression.ImageDPI() > 0 {
		bounds := img.Bounds()
		target = compressionImageSize(math.Hypot(m[0][0], m[1][0])*float64(bounds.Dx()), math.Hypot(m[0][1], m[1][1])*float64(bounds.Dy()), s.renderer.Compression.ImageDPI())
	}
	key := svgImageKey{image: img, size: target}
	name, ok := s.imageNames[key]
	var resource SVGImage
	if !ok {
		resource, s.err = s.imageResource(ctx, img, target)
		if s.err != nil {
			return
		}
		if s.imageNames != nil {
			name = fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(resource.Data))
			resource.Name = name
			s.imageNames[key] = name
			if !s.seen[name] {
				s.seen[name] = true
				s.images = append(s.images, resource)
			}
		}
	}
	_, height := s.Size()
	size := img.Bounds().Size()
	m = m.Translate(0, float64(size.Y))
	matrix := Matrix{a: m[0][0], b: -m[1][0], c: -m[0][1], d: m[1][1], e: m[0][2], f: height - m[1][2]}
	writer := imageOutput{context: ctx, writer: s.writer}
	if _, s.err = fmt.Fprintf(writer, `<image transform="matrix(%s)" width="%d" height="%d" xlink:href="`, matrix.String(), size.X, size.Y); s.err != nil {
		return
	}
	if s.imageNames != nil {
		if _, s.err = io.WriteString(writer, name); s.err != nil {
			return
		}
	} else {
		if _, s.err = fmt.Fprintf(writer, "data:%s;base64,", resource.MIME); s.err != nil {
			return
		}
		encoder := base64.NewEncoder(base64.StdEncoding, writer)
		if _, err := encoder.Write(resource.Data); err != nil {
			s.err = err
			return
		}
		if err := encoder.Close(); err != nil {
			s.err = err
			return
		}
	}
	_, s.err = io.WriteString(writer, `"/>`)
	if s.err == nil {
		s.err = ctx.Err()
	}
}

// imageResource 保留原编码或压缩已解码像素，先降采样再编码并响应取消
// 优化失败时回退原图PNG，取消时终止，不改变SVG中的图片尺寸和变换
// 入参: ctx 取消上下文, img 原图, target 压缩像素需求
// 返回: SVGImage 图片资源, error 编码或取消错误
func (s *svgResourceRenderer) imageResource(ctx context.Context, img image.Image, target image.Point) (SVGImage, error) {
	options := CompressionOptions{}
	if s.renderer != nil {
		options = s.renderer.Compression
	}
	resource := SVGImage{MIME: "image/png"}
	if encoded, ok := img.(*canvasimage.Image); ok {
		resource.MIME, resource.Data = encoded.Mimetype, encoded.Bytes
		if options.Mode != CompressionUnchanged {
			data, err := pdfgo.OptimizeImageResource(ctx, resource.Data, options, target)
			if ctx.Err() != nil {
				return SVGImage{}, ctx.Err()
			}
			if err == nil {
				resource.Data = data
			}
		}
	} else {
		if options.Mode != CompressionUnchanged {
			pixels := img
			var err error
			if options.Mode == CompressionLossy {
				pixels, err = pdfgo.ResizeImage(ctx, img, target)
				if err != nil {
					return SVGImage{}, err
				}
			}
			resource.Data, err = pdfgo.EncodeImageResource(ctx, pixels, options)
			if err != nil {
				if ctx.Err() != nil {
					return SVGImage{}, ctx.Err()
				}
				resource.Data = nil
			}
		}
		if resource.Data == nil {
			var buffer bytes.Buffer
			if err := pdfgo.EncodePNG(ctx, &buffer, img); err != nil {
				return SVGImage{}, err
			}
			resource.Data = buffer.Bytes()
		}
	}
	if bytes.HasPrefix(resource.Data, []byte{255, 216}) {
		resource.MIME = "image/jpeg"
	}
	if err := ctx.Err(); err != nil {
		return SVGImage{}, err
	}
	return resource, nil
}

// RenderText 绘制文字并记录实际使用的字体资源
// 入参: text 文字对象, m 变换矩阵
func (s *svgResourceRenderer) RenderText(text *canvas.Text, m canvas.Matrix) {
	if s.embeddedFonts {
		s.SVG.RenderText(text, m)
		return
	}
	if text.Empty() {
		return
	}
	font := text.MostCommonFontFace().Font
	resource, ok := s.renderer.canvasState().svgFontCache[font]
	if !ok {
		data := basefont.SFNTData(font.Tables)
		resource = SVGFont{
			Name:   fmt.Sprintf("ofdgo-%x-%d", sha256.Sum256(data), font.Style()),
			Weight: font.Style().CSS(),
			Style:  "normal",
			Data:   data,
		}
		if font.Style().Italic() {
			resource.Style = "italic"
		}
		s.renderer.canvasState().svgFontCache[font] = resource
	}
	if !s.seen[resource.Name] {
		s.seen[resource.Name] = true
		s.fonts = append(s.fonts, resource)
		fmt.Fprintf(&s.styles, ".%s{font-family:%s!important}", resource.Name, resource.Name)
	}
	s.SetClass(resource.Name)
	s.SVG.RenderText(text, m)
	s.SetClass()
}

// renderSVGResources 渲染SVG并收集外部资源
// 入参: page 页面内容, writer 输出流, images 是否分离图片, objects 是否标识页面直接对象
// 返回: SVGResources 外部资源, error 错误信息
func (r *Renderer) renderSVGResources(page *PageContent, writer io.Writer, images, objects bool) (SVGResources, error) {
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	options := svg.DefaultOptions
	options.EmbedFonts = false
	buffer := bufio.NewWriter(writer)
	s := &svgResourceRenderer{
		SVG:      svg.New(buffer, box.W, box.H, &options),
		renderer: r,
		writer:   buffer,
		seen:     make(map[string]bool),
	}
	io.WriteString(buffer, svgCreatorMetadata)
	io.WriteString(buffer, `<g style="image-orientation:none">`)
	if images {
		s.imageNames = make(map[svgImageKey]string)
	}
	if objects {
		s.objects = make(map[*GraphicObject]string)
		for i := range page.Content.Layer {
			for j := range page.Content.Layer[i].Objects {
				object := &page.Content.Layer[i].Objects[j]
				s.objects[object] = editorObjectID(*object)
			}
		}
		if err := r.renderCanvasPageToContext(canvas.NewContext(s), page, !r.TransparentBackground); err != nil {
			return SVGResources{}, err
		}
	} else {
		c, err := r.renderCanvasPage(page)
		if err != nil {
			return SVGResources{}, err
		}
		c.RenderTo(s)
	}
	if s.err != nil {
		return SVGResources{}, s.err
	}
	io.WriteString(buffer, `</g>`)
	s.SetCustomStyle(s.styles.String())
	if err := s.Close(); err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Fonts: s.fonts, Images: s.images}, buffer.Flush()
}

// beginObject 标记直接对象及复合或注解成员，模板图元不单独标记
// 入参: object 图形对象
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginObject(object *GraphicObject) bool {
	id := s.objects[object]
	attribute := "data-ofd-object"
	if id == "" {
		if len(s.objectStack) == 0 || !s.objectStack[len(s.objectStack)-1].composite {
			return false
		}
		parent := &s.objectStack[len(s.objectStack)-1]
		id = fmt.Sprintf("%s/%d", parent.path, parent.next)
		parent.next++
		attribute = "data-ofd-child"
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: id, composite: object.Type == "CompositeObject" || object.Type == "CompositeGraphicUnit"})
	fmt.Fprintf(s.writer, `<g %s="%s">`, attribute, html.EscapeString(id))
	return true
}

// endObject 结束对象分组
func (s *svgResourceRenderer) endObject() {
	s.objectStack = s.objectStack[:len(s.objectStack)-1]
	fmt.Fprint(s.writer, `</g>`)
}

// beginAnnotation 标记完整注解外观，内部图元保持原有层级和绘制顺序
// 入参: id 注解标识
// 返回: bool 是否写入分组
func (s *svgResourceRenderer) beginAnnotation(id string) bool {
	if s.objects == nil {
		return false
	}
	s.objectStack = append(s.objectStack, svgObjectGroup{path: "annotation:" + id, composite: true})
	fmt.Fprintf(s.writer, `<g data-ofd-object="annotation:%s">`, html.EscapeString(id))
	return true
}

// skipObjects 为不可见资源保留内部成员序号
// 入参: count 直接成员数量
func (s *svgResourceRenderer) skipObjects(count int) {
	if len(s.objectStack) > 0 {
		s.objectStack[len(s.objectStack)-1].next += count
	}
}
