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
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// imagePageInfo 图片方向与像素密度，不改变原始编码
type imagePageInfo struct {
	orientation int
	xdpi, ydpi  float64
}

// imagePageMetadata 读取图片方向与密度，TIFF方向由解码器处理，JPEG的EXIF密度优先于JFIF
// 入参: data 图片数据, format 图像格式, dpi 缺少物理密度时使用的DPI
// 返回: imagePageInfo 方向与横纵DPI, error 元数据错误
func imagePageMetadata(data []byte, format string, dpi float64) (imagePageInfo, error) {
	info := imagePageInfo{orientation: 1, xdpi: dpi, ydpi: dpi}
	var exif []byte
	var directory uint32
	if format == "tiff" {
		layout, err := readTIFFLayout(bytes.NewReader(data))
		if err != nil {
			return info, err
		}
		directory = layout.offset
		exif = data
	}
	if format == "bmp" && len(data) >= 54 && binary.LittleEndian.Uint32(data[14:18]) >= 40 {
		x := int32(binary.LittleEndian.Uint32(data[38:42]))
		y := int32(binary.LittleEndian.Uint32(data[42:46]))
		if x > 0 && y > 0 {
			info.xdpi, info.ydpi = float64(x)*0.0254, float64(y)*0.0254
		}
	}
	if format == "png" {
		for offset := 8; offset+12 <= len(data); {
			n := uint64(binary.BigEndian.Uint32(data[offset:]))
			if n > uint64(len(data)-offset-12) {
				return info, fmt.Errorf("truncated PNG chunk")
			}
			end := offset + 8 + int(n)
			kind, payload := string(data[offset+4:offset+8]), data[offset+8:end]
			if kind == "pHYs" || kind == "eXIf" {
				if crc32.ChecksumIEEE(data[offset+4:end]) != binary.BigEndian.Uint32(data[end:]) {
					return info, fmt.Errorf("invalid PNG %s checksum", kind)
				}
			}
			if kind == "pHYs" && len(payload) == 9 {
				x, y := float64(binary.BigEndian.Uint32(payload)), float64(binary.BigEndian.Uint32(payload[4:]))
				if x > 0 && y > 0 {
					if payload[8] == 1 {
						info.xdpi, info.ydpi = x*0.0254, y*0.0254
					} else if payload[8] == 0 {
						info.ydpi = dpi * y / x
					}
				}
			} else if kind == "eXIf" {
				exif = payload
			}
			offset = end + 4
			if kind == "IEND" {
				break
			}
		}
	} else if format == "jpeg" {
		for offset := 2; offset < len(data); {
			if data[offset] != 0xff {
				return info, fmt.Errorf("invalid JPEG marker")
			}
			for offset < len(data) && data[offset] == 0xff {
				offset++
			}
			if offset >= len(data) {
				break
			}
			marker := data[offset]
			offset++
			if marker == 0xda || marker == 0xd9 {
				break
			}
			if marker == 0x01 || marker >= 0xd0 && marker <= 0xd7 {
				continue
			}
			if offset+2 > len(data) {
				return info, fmt.Errorf("truncated JPEG marker")
			}
			n := int(binary.BigEndian.Uint16(data[offset:]))
			if n < 2 || n > len(data)-offset {
				return info, fmt.Errorf("truncated JPEG segment")
			}
			payload := data[offset+2 : offset+n]
			if marker == 0xe0 && len(payload) >= 14 && bytes.Equal(payload[:5], []byte("JFIF\x00")) {
				x, y := float64(binary.BigEndian.Uint16(payload[8:])), float64(binary.BigEndian.Uint16(payload[10:]))
				if x > 0 && y > 0 {
					switch payload[7] {
					case 0:
						info.ydpi = dpi * y / x
					case 1:
						info.xdpi, info.ydpi = x, y
					case 2:
						info.xdpi, info.ydpi = x*2.54, y*2.54
					}
				}
			} else if marker == 0xe1 && bytes.HasPrefix(payload, []byte("Exif\x00\x00")) {
				exif = payload[6:]
			}
			offset += n
		}
	}
	if len(exif) != 0 {
		orientation, x, y, err := imageExifMetadata(exif, dpi, directory)
		if err != nil {
			return info, err
		}
		info.orientation = orientation
		if (format == "jpeg" || format == "tiff") && x > 0 && y > 0 {
			info.xdpi, info.ydpi = x, y
		}
		if format == "tiff" {
			if orientation >= 5 {
				info.xdpi, info.ydpi = info.ydpi, info.xdpi
			}
			info.orientation = 1
		}
	}
	return info, nil
}

// imageExifMetadata 只读取主图IFD中的方向和密度，不跟随缩略图与其他目录
// 入参: data TIFF或EXIF数据, dpi 无绝对密度单位时的横向DPI，零值不换算, directory 目录偏移，零值使用首目录
// 返回: int EXIF方向值，未声明时为1, float64 横向DPI, float64 纵向DPI, error 元数据错误
func imageExifMetadata(data []byte, dpi float64, directory uint32) (int, float64, float64, error) {
	invalid := fmt.Errorf("invalid EXIF image metadata")
	if len(data) < 8 {
		return 0, 0, 0, invalid
	}
	var order binary.ByteOrder
	switch string(data[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0, 0, 0, invalid
	}
	if order.Uint16(data[2:]) != 42 {
		return 0, 0, 0, invalid
	}
	offset := uint64(order.Uint32(data[4:]))
	if directory != 0 {
		offset = uint64(directory)
	}
	if offset < 8 || offset+2 > uint64(len(data)) {
		return 0, 0, 0, invalid
	}
	count := uint64(order.Uint16(data[offset:]))
	if count*12+offset+6 > uint64(len(data)) {
		return 0, 0, 0, invalid
	}
	orientation, unit := 1, 2
	var x, y float64
	for i := uint64(0); i < count; i++ {
		entry := data[offset+2+i*12 : offset+14+i*12]
		tag, kind, count := order.Uint16(entry), order.Uint16(entry[2:]), order.Uint32(entry[4:])
		switch tag {
		case 0x112, 0x128:
			if kind != 3 || count != 1 {
				return 0, 0, 0, invalid
			}
			value := int(order.Uint16(entry[8:]))
			if tag == 0x112 {
				if value < 1 || value > 8 {
					return 0, 0, 0, invalid
				}
				orientation = value
			} else {
				unit = value
			}
		case 0x11a, 0x11b:
			at := uint64(order.Uint32(entry[8:]))
			if kind != 5 || count != 1 || at+8 > uint64(len(data)) {
				return 0, 0, 0, invalid
			}
			n, d := order.Uint32(data[at:]), order.Uint32(data[at+4:])
			if n > 0 && d > 0 {
				if tag == 0x11a {
					x = float64(n) / float64(d)
				} else {
					y = float64(n) / float64(d)
				}
			}
		}
	}
	if unit == 3 {
		x, y = x*2.54, y*2.54
	} else if unit == 1 && dpi > 0 && x > 0 && y > 0 {
		x, y = dpi, dpi*y/x
	} else if unit != 2 {
		x, y = 0, 0
	}
	return orientation, x, y, nil
}
