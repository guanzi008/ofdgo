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
	"crypto/sha256"
	"fmt"
	"io"
)

// PreviewOptions 指定页面显示方式，光栅预览不提供SVG对象分组
type PreviewOptions struct {
	Raster  bool
	Objects bool
}

// RenderPreview 生成统一SVG容器及独立资源，原生程序与WebUI使用相同绘制入口
// 入参: page 页面内容, writer 输出流, options 显示方式
// 返回: SVGResources 需由调用方映射地址的资源, error 错误信息
func (r *Renderer) RenderPreview(page *PageContent, writer io.Writer, options PreviewOptions) (SVGResources, error) {
	if !options.Raster {
		if options.Objects {
			return r.RenderToSVGWithObjects(page, writer)
		}
		return r.RenderToSVGWithResources(page, writer)
	}
	if options.Objects {
		return SVGResources{}, fmt.Errorf("raster preview does not provide SVG object groups")
	}
	box, err := r.GetPageBox(page)
	if err != nil {
		return SVGResources{}, err
	}
	var pixels bytes.Buffer
	if err := r.RenderToPNG(page, &pixels); err != nil {
		return SVGResources{}, err
	}
	name := fmt.Sprintf("ofdgo-image-%x", sha256.Sum256(pixels.Bytes()))
	_, err = fmt.Fprintf(writer, `<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" width="%gmm" height="%gmm" viewBox="0 0 %g %g"><image width="%g" height="%g" preserveAspectRatio="none" xlink:href="%s"/></svg>`, box.W, box.H, box.W, box.H, box.W, box.H, name)
	if err != nil {
		return SVGResources{}, err
	}
	return SVGResources{Images: []SVGImage{{Name: name, MIME: "image/png", Data: pixels.Bytes()}}}, nil
}

// RenderFontPreview 将指定字体的前32个可见字形绘制为独立SVG，不依赖字符映射或浏览器字体
// 排除缺字字形，外部字体须精确匹配，不修改原始资源或使用无关回退字体
// 入参: ctx 取消上下文, id 字体资源标识, writer 输出流
// 返回: error 字体、轮廓、取消或写入错误
func (r *Renderer) RenderFontPreview(ctx context.Context, id string, writer io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	view := *r
	view.pageContext = ctx
	resolved, err := view.ResolveFont(id, true)
	if err != nil {
		return err
	}
	font := &PreparedFont{ResolvedFont: resolved}
	if definition := view.Reader.fontDefinition(id); definition != nil && definition.FontFile != "" {
		font, err = view.PrepareFont(id)
		if err != nil {
			return err
		}
	}
	metrics, err := view.preparedMetrics(font)
	if err != nil {
		return err
	}
	outlines, ok := metrics.(FontOutlines)
	if !ok {
		return fmt.Errorf("font outlines: %w", ErrBackendUnavailable)
	}
	var paths bytes.Buffer
	count := 0
	for glyph := uint16(1); glyph < metrics.NumGlyphs() && count < 32; glyph++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		outline, err := outlines.GlyphOutline(glyph, 24)
		if err != nil {
			return err
		}
		box, err := outline.Bounds()
		if err != nil {
			return err
		}
		if box.W <= 0 || box.H <= 0 {
			continue
		}
		path, err := outline.SVG()
		if err != nil {
			return err
		}
		scale := min(1.0, 28/box.W, 32/box.H)
		x := float64(count%8)*36 + (36-box.W*scale)/2 - box.X*scale
		y := float64(count/8)*40 + (40-box.H*scale)/2 - box.Y*scale
		fmt.Fprintf(&paths, `<path transform="translate(%g %g) scale(%g)" d="%s"/>`, x, y, scale, path)
		count++
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("font has no visible glyphs")
	}
	width, height := min(count, 8)*36, (count+7)/8*40
	_, err = fmt.Fprintf(writer, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">%s</svg>`, width, height, width, height, paths.Bytes())
	return err
}
