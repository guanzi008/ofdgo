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
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

// tiffImageLayout 保存单页TIFF的尺寸、方向、采样及压缩参数
type tiffImageLayout struct {
	width, height int
	orientation   int
	offset, next  uint32
	reduced, mask bool
	planar        bool
	samples       uint32
	depth         uint32
	fillOrder     uint32
	compression   uint32
	t4, t6        uint32
}

// errTIFFRandomAccess 表示目录链需要回读，仅流式输入需重新打开资源
var errTIFFRandomAccess = errors.New("TIFF directory requires random access")

// tiffDirectoryInput 只替换解码视图中的首目录偏移，不复制或改写原始资源
type tiffDirectoryInput struct {
	reader    *bytes.Reader
	directory [4]byte
	ctx       context.Context
}

// orientedTIFFImage 按方向映射像素坐标，复用原图数据与颜色精度
type orientedTIFFImage struct {
	source image.Image
	tiffImageLayout
}

// isTIFFData 识别标准TIFF字节序与版本标识
// 入参: data 文件头或完整编码
// 返回: bool 是否为TIFF 6.0编码
func isTIFFData(data []byte) bool {
	return len(data) >= 4 && (string(data[:4]) == "II\x2a\x00" || string(data[:4]) == "MM\x00\x2a")
}

// readTIFFLayout 选择单页TIFF的主图，允许缩略图目录并拒绝循环和多个主图
// 入参: input 从文件头开始的输入流
// 返回: tiffImageLayout 存储尺寸与方向, error 文件结构或方向错误
func readTIFFLayout(input io.Reader) (tiffImageLayout, error) {
	var result tiffImageLayout
	var header [8]byte
	if _, err := io.ReadFull(input, header[:]); err != nil {
		return result, err
	}
	if !isTIFFData(header[:]) {
		return result, fmt.Errorf("invalid TIFF header")
	}
	var order binary.ByteOrder = binary.LittleEndian
	if header[0] == 'M' {
		order = binary.BigEndian
	}
	offset := int64(order.Uint32(header[4:]))
	position := int64(8)
	var seen map[uint32]bool
	for offset != 0 {
		if seen != nil && seen[uint32(offset)] {
			return result, fmt.Errorf("cyclic TIFF directory chain")
		}
		layout, end, err := readTIFFDirectory(input, order, offset, position)
		if err != nil {
			return result, err
		}
		if layout.mask {
			return result, fmt.Errorf("TIFF transparency mask directories are not supported")
		}
		if !layout.reduced || layout.next == 0 && seen == nil {
			if result.offset != 0 {
				return result, fmt.Errorf("multiple TIFF pages are not supported in OFD resources")
			}
			result = layout
		}
		if layout.next != 0 {
			if seen == nil {
				seen = make(map[uint32]bool)
			}
			seen[uint32(offset)] = true
		}
		offset, position = int64(layout.next), end
	}
	if result.offset == 0 {
		return result, fmt.Errorf("missing TIFF main image")
	}
	return result, nil
}

// readTIFFDirectory 读取目录中的图像布局和子文件类型，不读取像素
// 入参: input 输入流, order 字节序, offset 目录偏移, position 当前流位置
// 返回: tiffImageLayout 目录布局, int64 读取后的流位置, error 结构或读取错误
func readTIFFDirectory(input io.Reader, order binary.ByteOrder, offset, position int64) (tiffImageLayout, int64, error) {
	var result tiffImageLayout
	var header [4]byte
	if offset < 8 {
		return result, position, fmt.Errorf("invalid TIFF directory offset")
	}
	if seeker, ok := input.(io.Seeker); ok {
		if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
			return result, position, err
		}
	} else {
		if offset < position {
			return result, position, errTIFFRandomAccess
		}
		if _, err := io.CopyN(io.Discard, input, offset-position); err != nil {
			return result, position, err
		}
	}
	if _, err := io.ReadFull(input, header[:2]); err != nil {
		return result, position, err
	}
	count := int(order.Uint16(header[:2]))
	result.orientation, result.offset, result.samples, result.fillOrder = 1, uint32(offset), 1, 1
	result.depth = 1
	result.compression = 1
	var entry [12]byte
	var previous uint16
	var subfile, legacy uint32
	var hasSubfile bool
	for i := range count {
		if _, err := io.ReadFull(input, entry[:]); err != nil {
			return result, position, err
		}
		tag, kind, number := order.Uint16(entry[:2]), order.Uint16(entry[2:4]), order.Uint32(entry[4:8])
		if i > 0 && tag <= previous {
			return result, position, fmt.Errorf("invalid TIFF directory order")
		}
		previous = tag
		if tag == 258 && kind == 3 && number == 1 {
			result.depth = uint32(order.Uint16(entry[8:10]))
		}
		if tag != 254 && tag != 255 && tag != 256 && tag != 257 && tag != 259 && tag != 262 && tag != 266 && tag != 274 && tag != 277 && tag != 284 && tag != 292 && tag != 293 {
			continue
		}
		if number != 1 || kind != 3 && kind != 4 || (tag == 255 || tag == 259 || tag == 262 || tag == 266 || tag == 274 || tag == 277 || tag == 284) && kind != 3 || (tag == 254 || tag == 292 || tag == 293) && kind != 4 {
			return result, position, fmt.Errorf("invalid TIFF image layout")
		}
		value := uint64(order.Uint32(entry[8:12]))
		if kind == 3 {
			value = uint64(order.Uint16(entry[8:10]))
		}
		if (tag == 256 || tag == 257) && (value == 0 || value > uint64(int(^uint(0)>>1))) {
			return result, position, fmt.Errorf("invalid TIFF dimensions")
		}
		switch tag {
		case 254:
			subfile, hasSubfile = uint32(value), true
		case 255:
			legacy = uint32(value)
		case 256:
			result.width = int(value)
		case 257:
			result.height = int(value)
		case 259:
			result.compression = uint32(value)
		case 262:
			result.mask = value == 4
		case 266:
			if value != 1 && value != 2 {
				return result, position, fmt.Errorf("invalid TIFF fill order")
			}
			result.fillOrder = uint32(value)
		case 274:
			if value < 1 || value > 8 {
				return result, position, fmt.Errorf("invalid TIFF orientation")
			}
			result.orientation = int(value)
		case 277:
			if value == 0 {
				return result, position, fmt.Errorf("invalid TIFF sample count")
			}
			result.samples = uint32(value)
		case 284:
			if value != 1 && value != 2 {
				return result, position, fmt.Errorf("invalid TIFF planar configuration")
			}
			result.planar = value == 2
		case 292:
			result.t4 = uint32(value)
		case 293:
			result.t6 = uint32(value)
		}
	}
	if _, err := io.ReadFull(input, header[:4]); err != nil {
		return result, position, err
	}
	result.next = order.Uint32(header[:4])
	if hasSubfile {
		result.reduced, result.mask = subfile&1 != 0, result.mask || subfile&4 != 0
	} else {
		result.reduced = legacy == 2
	}
	if result.width == 0 || result.height == 0 {
		return result, position, fmt.Errorf("missing TIFF dimensions")
	}
	return result, offset + 6 + int64(count)*12, nil
}

// newTIFFDirectoryInput 创建以指定目录作为主图的只读解码视图
// 入参: data 已校验的TIFF编码, offset 主图目录偏移
// 返回: *tiffDirectoryInput 随机访问输入
func newTIFFDirectoryInput(data []byte, offset uint32) *tiffDirectoryInput {
	input := &tiffDirectoryInput{reader: bytes.NewReader(data)}
	var order binary.ByteOrder = binary.LittleEndian
	if data[0] == 'M' {
		order = binary.BigEndian
	}
	order.PutUint32(input.directory[:], offset)
	return input
}

// patch 替换读取片段中与首目录指针重叠的部分
// 入参: data 读取结果, offset 文件偏移
func (r *tiffDirectoryInput) patch(data []byte, offset int64) {
	for i := range r.directory {
		at := int64(4+i) - offset
		if at >= 0 && at < int64(len(data)) {
			data[at] = r.directory[i]
		}
	}
}

// Read 读取图像编码并覆盖首目录指针
// 入参: data 接收缓冲
// 返回: int 读取字节数, error 读取错误
func (r *tiffDirectoryInput) Read(data []byte) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	offset := r.reader.Size() - int64(r.reader.Len())
	n, err := r.reader.Read(data)
	r.patch(data[:n], offset)
	return n, err
}

// ReadAt 随机读取图像编码并覆盖首目录指针
// 入参: data 接收缓冲, offset 文件偏移
// 返回: int 读取字节数, error 读取错误
func (r *tiffDirectoryInput) ReadAt(data []byte, offset int64) (int, error) {
	if r.ctx != nil {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
	}
	n, err := r.reader.ReadAt(data, offset)
	r.patch(data[:n], offset)
	return n, err
}

// size 返回应用方向后的显示尺寸
// 返回: image.Point 像素宽高
func (l tiffImageLayout) size() image.Point {
	if l.orientation >= 5 {
		return image.Pt(l.height, l.width)
	}
	return image.Pt(l.width, l.height)
}

// Bounds 返回从零开始的显示区域
// 返回: image.Rectangle 显示区域
func (s *orientedTIFFImage) Bounds() image.Rectangle {
	return image.Rectangle{Max: s.size()}
}

// ColorModel 返回原始颜色模型
// 返回: color.Model 颜色模型
func (s *orientedTIFFImage) ColorModel() color.Model { return s.source.ColorModel() }

// sourcePoint 将显示坐标映射为存储坐标
// 入参: x 横向显示坐标, y 纵向显示坐标
// 返回: image.Point 原始坐标, bool 是否位于显示区域内
func (s *orientedTIFFImage) sourcePoint(x, y int) (image.Point, bool) {
	if !image.Pt(x, y).In(s.Bounds()) {
		return image.Point{}, false
	}
	w, h := s.width, s.height
	switch s.orientation {
	case 2:
		x = w - 1 - x
	case 3:
		x, y = w-1-x, h-1-y
	case 4:
		y = h - 1 - y
	case 5:
		x, y = y, x
	case 6:
		x, y = y, h-1-x
	case 7:
		x, y = w-1-y, h-1-x
	case 8:
		x, y = w-1-y, x
	}
	b := s.source.Bounds()
	return image.Pt(b.Min.X+x, b.Min.Y+y), true
}

// At 返回原图颜色，不重新采样或量化
// 入参: x 横向显示坐标, y 纵向显示坐标
// 返回: color.Color 像素颜色，区域外透明
func (s *orientedTIFFImage) At(x, y int) color.Color {
	p, ok := s.sourcePoint(x, y)
	if !ok {
		return color.NRGBA64{}
	}
	return s.source.At(p.X, p.Y)
}

// NRGBA64At 读取非预乘十六位颜色，保留透明像素中的原始颜色
// 入参: x 横向显示坐标, y 纵向显示坐标
// 返回: color.NRGBA64 像素颜色
func (s *orientedTIFFImage) NRGBA64At(x, y int) color.NRGBA64 {
	p, ok := s.sourcePoint(x, y)
	if !ok {
		return color.NRGBA64{}
	}
	return imageNRGBA64At(s.source, p.X, p.Y)
}

// RGBA64At 返回预乘十六位像素，保留原始颜色精度
// 入参: x 横向显示坐标, y 纵向显示坐标
// 返回: color.RGBA64 像素颜色
func (s *orientedTIFFImage) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}
