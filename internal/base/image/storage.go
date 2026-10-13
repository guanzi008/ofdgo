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

package image

import "image"

// StorageCost 预留十六位像素空间，并计入编码、调色板与底层像素缓冲容量
// 包装图像可实现BackingImage() image.Image，使底层共享缓冲计入预算
// 入参: img 图片资源, limit 缓存预算
// 返回: int 估算字节数或超预算标记
func StorageCost(img image.Image, limit int) int {
	cost := 256
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if limit < cost || w <= 0 || h <= 0 || w > (limit-cost)/8/h {
		return limit + 1
	}
	pixels := w * h * 8
	if encoded, ok := img.(*EncodedImage); ok {
		if cap(encoded.data) > limit-cost-pixels {
			return limit + 1
		}
		return cost + pixels + cap(encoded.data)
	}
	var buffers [5]int
	switch source := img.(type) {
	case *image.RGBA:
		buffers[0] = cap(source.Pix)
	case *image.RGBA64:
		buffers[0] = cap(source.Pix)
	case *image.NRGBA:
		buffers[0] = cap(source.Pix)
	case *image.NRGBA64:
		buffers[0] = cap(source.Pix)
	case *image.Gray:
		buffers[0] = cap(source.Pix)
	case *image.Gray16:
		buffers[0] = cap(source.Pix)
	case *image.Alpha:
		buffers[0] = cap(source.Pix)
	case *image.Alpha16:
		buffers[0] = cap(source.Pix)
	case *image.CMYK:
		buffers[0] = cap(source.Pix)
	case *image.Paletted:
		if cap(source.Palette) > (limit-cost)/16 {
			return limit + 1
		}
		buffers[0], buffers[1] = cap(source.Pix), cap(source.Palette)*16
	case *image.YCbCr:
		buffers = [5]int{cap(source.Y), cap(source.Cb), cap(source.Cr)}
	case *image.NYCbCrA:
		buffers = [5]int{cap(source.Y), cap(source.Cb), cap(source.Cr), cap(source.A)}
	case *planarTIFFImage:
		if cap(source.palette) > (limit-cost)/8 {
			return limit + 1
		}
		cost += cap(source.palette) * 8
		if cap(source.planes) > (limit-cost)/16 || len(source.planes) > len(buffers) {
			return limit + 1
		}
		cost += cap(source.planes) * 16
		for i, plane := range source.planes {
			switch plane := plane.(type) {
			case *image.Gray:
				buffers[i] = cap(plane.Pix)
			case *image.Gray16:
				buffers[i] = cap(plane.Pix)
			default:
				buffers[i] = StorageCost(plane, limit-cost)
			}
		}
	case *tiffFaxImage:
		if cap(source.blocks) > (limit-cost)/16 {
			return limit + 1
		}
		cost += cap(source.blocks) * 16
		for _, block := range source.blocks {
			size := StorageCost(block, limit-cost)
			if size > limit-cost {
				return limit + 1
			}
			cost += size
		}
	case *orientedTIFFImage:
		buffers[0] = StorageCost(source.source, limit-cost)
	case *maskedTIFFImage:
		buffers[0] = StorageCost(source.source, limit-cost)
		buffers[1] = StorageCost(source.mask, limit-cost)
	case *transparentEdgeImage:
		buffers[0] = StorageCost(source.source, limit-cost)
	case *transparentEdgeImage64:
		buffers[0] = StorageCost(source.source, limit-cost)
	case interface{ BackingImage() image.Image }:
		buffers[0] = StorageCost(source.BackingImage(), limit-cost)
	}
	storage := 0
	for _, size := range buffers {
		if size > limit-cost-storage {
			return limit + 1
		}
		storage += size
	}
	if pixels > limit-cost {
		return limit + 1
	}
	return cost + max(pixels, storage)
}
