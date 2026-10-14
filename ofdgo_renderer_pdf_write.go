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
	"io"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// writePDF 传递后端资源记录及OFD导航修正，写出最终PDF
// 入参: ctx 取消上下文, data 后端PDF数据, writer 输出流, optimization 优化配置
// 返回: error 资源、压缩或写入错误
func (r *pdfRenderer) writePDF(ctx context.Context, data []byte, writer io.Writer, optimization pdfgo.OptimizeOptions) error {
	if r.imageError != nil {
		return r.imageError
	}
	options := codecpdf.WriteOptions{
		Images:       r.images,
		Text:         r.text,
		ExactImages:  r.exactImages,
		ExactText:    r.exactText,
		Optimization: optimization,
	}
	if r.navigation != nil && r.navigation.nativeWrite {
		options.Links = make(map[int][]*pdfgo.LinkRegion, len(r.navigation.Link))
		for page, links := range r.navigation.Link {
			regions := make([]*pdfgo.LinkRegion, len(links))
			for i, link := range links {
				regions[i] = link.Region
			}
			options.Links[page] = regions
		}
		options.PrepareRewrite = r.navigation.prepareRewrite
	}
	return codecpdf.Write(ctx, data, writer, options)
}
