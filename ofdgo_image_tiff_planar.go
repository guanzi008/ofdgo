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
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
)

// tiffPlaneInput 将单分量目录附加到只读视图，像素仍从原始编码读取
type tiffPlaneInput struct {
	source, directory []byte
	position          int64
	order             binary.ByteOrder
	ctx               context.Context
}

// planarTIFFImage 组合独立分量或映射调色板，不额外分配交错像素缓冲
type planarTIFFImage struct {
	planes  []image.Image
	bounds  image.Rectangle
	depth   uint32
	packed  [4]uint32
	alpha   uint32
	gray    bool
	palette []color.NRGBA64
}

// decodeTIFFImage 按存储方式解码主图并统一显示方向
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 主图布局
// 返回: image.Image 图像, error 解码或取消错误
func decodeTIFFImage(ctx context.Context, data []byte, layout tiffImageLayout) (image.Image, error) {
	if layout.compression >= 2 && layout.compression <= 4 {
		pixels, err := decodeTIFFFax(ctx, data, layout)
		if err == nil && layout.orientation != 1 {
			pixels = &orientedTIFFImage{source: pixels, tiffImageLayout: layout}
		}
		return pixels, err
	}
	ranges, err := tiffBitOrderRanges(ctx, data, layout)
	if err != nil {
		return nil, err
	}
	var pixels image.Image
	if layout.planar && layout.samples > 1 || layout.samples == 1 && (layout.depth == 2 || layout.depth == 4) {
		result, directories, err := tiffPlaneDirectories(ctx, data, layout)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			input := &tiffPlaneInput{source: data, directory: directory, order: binary.LittleEndian, ctx: ctx}
			if data[0] == 'M' {
				input.order = binary.BigEndian
			}
			plane, err := decodeTIFFBitOrder(input, ranges)
			if err != nil {
				return nil, err
			}
			result.planes = append(result.planes, plane)
		}
		pixels = result
	} else {
		input := newTIFFDirectoryInput(data, layout.offset)
		input.ctx = ctx
		var err error
		pixels, err = decodeTIFFBitOrder(input, ranges)
		if err != nil {
			return nil, err
		}
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if layout.orientation != 1 {
		pixels = &orientedTIFFImage{source: pixels, tiffImageLayout: layout}
	}
	return pixels, nil
}

// tiffPlaneDirectories 为颜色、透明度或调色板索引构建单通道TIFF目录
// 入参: ctx 取消上下文，可为nil, data TIFF编码, layout 已校验的主图布局
// 返回: *planarTIFFImage 像素布局, [][]byte 各分量目录, error 字段或布局错误
func tiffPlaneDirectories(ctx context.Context, data []byte, layout tiffImageLayout) (*planarTIFFImage, [][]byte, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	if uint64(len(data))+65536*12+8 > math.MaxUint32 {
		return nil, nil, fmt.Errorf("TIFF plane directory offset exceeds format limit")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	offset := int(layout.offset)
	count := int(order.Uint16(data[offset:]))
	entries := make(map[uint16][]byte, count)
	for i := range count {
		entry := data[offset+2+i*12 : offset+14+i*12]
		entries[order.Uint16(entry)] = entry
	}
	value := func(tag uint16, fallback uint32) (uint32, error) {
		entry := entries[tag]
		if entry == nil {
			return fallback, nil
		}
		if order.Uint32(entry[4:]) != 1 {
			return 0, fmt.Errorf("invalid TIFF scalar field %d", tag)
		}
		return tiffPlaneValue(data, order, entry, 0)
	}
	samples, err := value(277, 1)
	if err != nil || samples < 1 || samples > 4 {
		return nil, nil, fmt.Errorf("invalid TIFF plane count")
	}
	photo, err := value(262, math.MaxUint32)
	if err != nil || photo > 3 || photo == 3 && samples != 1 {
		return nil, nil, fmt.Errorf("unsupported TIFF planar color model")
	}
	compression, err := value(259, 1)
	if err != nil {
		return nil, nil, err
	}
	fill, err := value(266, 1)
	if err != nil || fill != 1 && (fill != 2 || compression != 1 && compression != 3 && compression != 4) {
		return nil, nil, fmt.Errorf("unsupported TIFF plane fill order")
	}
	predictor, err := value(317, 1)
	if err != nil || predictor != 1 && predictor != 2 {
		return nil, nil, fmt.Errorf("unsupported TIFF plane predictor")
	}
	result := &planarTIFFImage{bounds: image.Rect(0, 0, layout.width, layout.height), gray: photo < 2}
	colors := uint32(3)
	if photo != 2 {
		colors = 1
	}
	if samples != colors {
		result.alpha, err = value(338, 0)
		if err != nil || samples != colors+1 || result.alpha != 1 && result.alpha != 2 {
			return nil, nil, fmt.Errorf("unsupported TIFF extra planes")
		}
	} else if entries[338] != nil {
		return nil, nil, fmt.Errorf("unexpected TIFF extra samples")
	}
	width, err := value(322, 0)
	if err != nil {
		return nil, nil, err
	}
	blocks := uint64(0)
	locations, lengths := uint16(273), uint16(279)
	if width != 0 {
		height, err := value(323, 0)
		if err != nil || height == 0 || width%16 != 0 || height%16 != 0 {
			return nil, nil, fmt.Errorf("invalid TIFF tile dimensions")
		}
		blocks = ((uint64(layout.width)-1)/uint64(width) + 1) * ((uint64(layout.height)-1)/uint64(height) + 1)
		locations, lengths = 324, 325
	} else {
		rows, err := value(278, math.MaxUint32)
		if err != nil || rows == 0 {
			return nil, nil, fmt.Errorf("invalid TIFF rows per strip")
		}
		blocks = (uint64(layout.height)-1)/uint64(rows) + 1
	}
	if blocks > math.MaxUint32/uint64(samples) {
		return nil, nil, fmt.Errorf("too many TIFF plane blocks")
	}
	for _, tag := range []uint16{locations, lengths} {
		entry := entries[tag]
		if entry == nil || uint64(order.Uint32(entry[4:])) != blocks*uint64(samples) || tag == 324 && order.Uint16(entry[2:]) != 4 {
			return nil, nil, fmt.Errorf("invalid TIFF plane block count")
		}
	}
	for i := uint32(0); i < uint32(blocks)*samples; i++ {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
		}
		at, err := tiffPlaneValue(data, order, entries[locations], i)
		if err != nil {
			return nil, nil, err
		}
		size, err := tiffPlaneValue(data, order, entries[lengths], i)
		if err != nil || size == 0 || uint64(at)+uint64(size) > uint64(len(data)) {
			return nil, nil, fmt.Errorf("truncated TIFF plane block")
		}
	}
	for _, tag := range []uint16{258, 339} {
		if entry := entries[tag]; entry != nil && (order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != samples) {
			return nil, nil, fmt.Errorf("invalid TIFF sample array")
		}
	}
	var directories [][]byte
	for plane := uint32(0); plane < samples; plane++ {
		depth := uint32(1)
		if entry := entries[258]; entry != nil {
			depth, err = tiffPlaneValue(data, order, entry, plane)
		}
		if err != nil || depth != 1 && depth != 2 && depth != 4 && depth != 8 && depth != 16 {
			return nil, nil, fmt.Errorf("unsupported TIFF plane depth")
		}
		result.depth = max(result.depth, depth)
		if depth < 8 && predictor != 1 {
			return nil, nil, fmt.Errorf("unsupported TIFF packed plane predictor")
		}
		if depth == 2 || depth == 4 {
			result.packed[plane] = depth
			if uint64(layout.width)*uint64(depth) > uint64(math.MaxInt)/uint64(layout.height) || uint64(layout.width)*uint64(depth) > math.MaxUint32 || uint64(width)*uint64(depth) > math.MaxUint32 {
				return nil, nil, fmt.Errorf("TIFF plane dimensions exceed format limit")
			}
			if compression == 2 || compression == 3 || compression == 4 {
				return nil, nil, fmt.Errorf("unsupported TIFF packed plane compression")
			}
		}
		if entry := entries[339]; entry != nil {
			format, err := tiffPlaneValue(data, order, entry, plane)
			if err != nil || format != 1 {
				return nil, nil, fmt.Errorf("unsupported TIFF sample format")
			}
		}
		if photo == 3 {
			entry := entries[320]
			if depth > 8 || entry == nil || order.Uint16(entry[2:]) != 3 || order.Uint32(entry[4:]) != 3<<depth {
				return nil, nil, fmt.Errorf("invalid TIFF color map")
			}
			n := uint32(1) << depth
			result.palette = make([]color.NRGBA64, n)
			for i := range n {
				var components [3]uint16
				for c := range components {
					v, err := tiffPlaneValue(data, order, entry, uint32(c)*n+i)
					if err != nil {
						return nil, nil, err
					}
					components[c] = uint16(v)
					if v%257 != 0 {
						result.depth = 16
					}
				}
				result.palette[i] = color.NRGBA64{R: components[0], G: components[1], B: components[2], A: 65535}
			}
		}
		directory := make([]byte, 2, 6+count*12)
		for i := range count {
			if ctx != nil {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			entry := bytes.Clone(data[offset+2+i*12 : offset+14+i*12])
			tag := order.Uint16(entry)
			if tag == 338 || tag == 320 {
				continue
			}
			scalar := uint32(0)
			switch tag {
			case 258:
				scalar = depth
				if result.packed[plane] != 0 {
					scalar = 1
				}
			case 256, 322:
				if result.packed[plane] != 0 {
					original, err := tiffPlaneValue(data, order, entry, 0)
					if err != nil {
						return nil, nil, err
					}
					order.PutUint16(entry[2:], 4)
					order.PutUint32(entry[4:], 1)
					order.PutUint32(entry[8:], original*depth)
				}
			case 262:
				scalar = 1
				if result.gray && plane == 0 && photo == 0 {
					scalar = 0
				}
			case 277, 284, 339:
				scalar = 1
			case locations, lengths:
				entry, err = tiffPlaneArray(data, order, entry, uint32(blocks)*plane, uint32(blocks))
				if err != nil {
					return nil, nil, err
				}
			}
			if tag == 258 || tag == 262 || tag == 277 || tag == 284 || tag == 339 {
				order.PutUint16(entry[2:], 3)
				order.PutUint32(entry[4:], 1)
				clear(entry[8:])
				order.PutUint16(entry[8:], uint16(scalar))
			}
			directory = append(directory, entry...)
		}
		order.PutUint16(directory, uint16((len(directory)-2)/12))
		directories = append(directories, append(directory, 0, 0, 0, 0))
	}
	return result, directories, nil
}

// tiffPlaneValue 读取SHORT或LONG数组中的一项，校验完整数组范围
// 入参: data 原编码, order 字节序, entry 目录项, index 元素序号
// 返回: uint32 元素值, error 类型或越界错误
func tiffPlaneValue(data []byte, order binary.ByteOrder, entry []byte, index uint32) (uint32, error) {
	kind, count := order.Uint16(entry[2:]), order.Uint32(entry[4:])
	if kind != 3 && kind != 4 || index >= count {
		return 0, fmt.Errorf("invalid TIFF plane array")
	}
	size := uint64(2)
	if kind == 4 {
		size = 4
	}
	values := entry[8:]
	if size*uint64(count) > 4 {
		at := uint64(order.Uint32(values))
		if at+size*uint64(count) > uint64(len(data)) {
			return 0, fmt.Errorf("truncated TIFF plane array")
		}
		values = data[at:]
	}
	if size == 2 {
		return uint32(order.Uint16(values[uint64(index)*size:])), nil
	}
	return order.Uint32(values[uint64(index)*size:]), nil
}

// tiffPlaneArray 将块索引缩减为指定分量的范围，较长数组继续借用原编码
// 入参: data 原编码, order 字节序, entry 目录项副本, first 起始序号, count 元素数
// 返回: []byte 修改后的目录项, error 类型或越界错误
func tiffPlaneArray(data []byte, order binary.ByteOrder, entry []byte, first, count uint32) ([]byte, error) {
	if _, err := tiffPlaneValue(data, order, entry, first+count-1); err != nil {
		return nil, err
	}
	size := uint32(2)
	if order.Uint16(entry[2:]) == 4 {
		size = 4
	}
	if uint64(count)*uint64(size) <= 4 {
		var values [4]byte
		for i := uint32(0); i < count; i++ {
			value, _ := tiffPlaneValue(data, order, entry, first+i)
			if size == 2 {
				order.PutUint16(values[i*size:], uint16(value))
			} else {
				order.PutUint32(values[i*size:], value)
			}
		}
		copy(entry[8:], values[:])
	} else {
		order.PutUint32(entry[8:], order.Uint32(entry[8:])+first*size)
	}
	order.PutUint32(entry[4:], count)
	return entry, nil
}

// Read 从组合视图顺序读取
// 入参: data 接收缓冲
// 返回: int 字节数, error 读取或取消错误
func (r *tiffPlaneInput) Read(data []byte) (int, error) {
	n, err := r.ReadAt(data, r.position)
	r.position += int64(n)
	return n, err
}

// ReadAt 从原始编码或附加目录读取并替换首目录指针
// 入参: data 接收缓冲, offset 文件偏移
// 返回: int 字节数, error 读取或取消错误
func (r *tiffPlaneInput) ReadAt(data []byte, offset int64) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	if offset < 0 {
		return 0, fmt.Errorf("negative TIFF read offset")
	}
	start := int64(len(r.source))
	directory := start + start%2
	written := 0
	if offset < start {
		written = copy(data, r.source[offset:])
	}
	if offset+int64(written) == start && start != directory && written < len(data) {
		data[written] = 0
		written++
	}
	if at := offset + int64(written) - directory; at >= 0 && at < int64(len(r.directory)) {
		written += copy(data[written:], r.directory[at:])
	}
	var pointer [4]byte
	r.order.PutUint32(pointer[:], uint32(directory))
	for i, value := range pointer {
		if at := int64(4+i) - offset; at >= 0 && at < int64(written) {
			data[at] = value
		}
	}
	if written != len(data) {
		return written, io.EOF
	}
	return written, nil
}

// Bounds 返回存储坐标范围
// 返回: image.Rectangle 图像范围
func (p *planarTIFFImage) Bounds() image.Rectangle { return p.bounds }

// ColorModel 按透明度类型和最高分量精度返回颜色模型
// 返回: color.Model 颜色模型
func (p *planarTIFFImage) ColorModel() color.Model {
	if p.gray && p.alpha == 0 {
		if p.depth > 8 {
			return color.Gray16Model
		}
		return color.GrayModel
	}
	if p.alpha == 1 {
		if p.depth > 8 {
			return color.RGBA64Model
		}
		return color.RGBAModel
	}
	if p.depth > 8 {
		return color.NRGBA64Model
	}
	return color.NRGBAModel
}

// components 直接读取灰度分量，保留原始透明度语义
// 入参: x 横坐标, y 纵坐标
// 返回: [4]uint16 红、绿、蓝和透明度分量
func (p *planarTIFFImage) components(x, y int) [4]uint16 {
	if !image.Pt(x, y).In(p.bounds) {
		return [4]uint16{}
	}
	values := [4]uint16{0, 0, 0, 65535}
	for i, plane := range p.planes {
		var value uint16
		switch plane := plane.(type) {
		case *image.Gray:
			if bits := p.packed[i]; bits != 0 {
				for bit := range int(bits) {
					value = value<<1 | uint16(plane.GrayAt(x*int(bits)+bit, y).Y>>7)
				}
				value *= 65535 / uint16(1<<bits-1)
			} else {
				value = uint16(plane.GrayAt(x, y).Y) * 257
			}
		case *image.Gray16:
			value = plane.Gray16At(x, y).Y
		default:
			v, _, _, _ := plane.At(x, y).RGBA()
			value = uint16(v)
		}
		if len(p.palette) != 0 {
			c := p.palette[uint32(value)*uint32(len(p.palette)-1)/65535]
			return [4]uint16{c.R, c.G, c.B, c.A}
		}
		if p.gray && i == 0 {
			values[0], values[1], values[2] = value, value, value
		} else if p.gray {
			values[3] = value
		} else {
			values[i] = value
		}
	}
	return values
}

// At 返回组合后的颜色，关联透明度保留预乘语义
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (p *planarTIFFImage) At(x, y int) color.Color {
	values := p.components(x, y)
	if p.alpha == 1 {
		return color.RGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
	}
	return color.NRGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}

// NRGBA64At 读取独立透明度颜色，保留非预乘分量中的隐藏颜色
// 入参: x 横坐标, y 纵坐标
// 返回: color.NRGBA64 像素颜色
func (p *planarTIFFImage) NRGBA64At(x, y int) color.NRGBA64 {
	values := p.components(x, y)
	if p.alpha == 1 && values[3] != 65535 {
		if values[3] == 0 {
			return color.NRGBA64{}
		}
		for i := range 3 {
			values[i] = uint16(uint32(values[i]) * 65535 / uint32(values[3]))
		}
	}
	return color.NRGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}

// RGBA64At 返回预乘十六位颜色，不分配临时颜色对象
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA64 像素颜色
func (p *planarTIFFImage) RGBA64At(x, y int) color.RGBA64 {
	values := p.components(x, y)
	if p.alpha != 1 && values[3] != 65535 {
		for i := range 3 {
			values[i] = uint16(uint32(values[i]) * uint32(values[3]) / 65535)
		}
	}
	return color.RGBA64{R: values[0], G: values[1], B: values[2], A: values[3]}
}
