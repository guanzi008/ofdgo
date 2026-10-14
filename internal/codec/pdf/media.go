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

package pdf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"path"
	"strings"

	"github.com/xiaoqidun/pdfgo"
)

// MediaFormat 获取媒体文件扩展名，URL参数和片段不参与格式判断
// 入参: file PDF文件说明
// 返回: string 媒体格式，未声明扩展名时为空
func MediaFormat(file pdfgo.FileSpecification) string {
	name := strings.ReplaceAll(file.Name, "\\", "/")
	if file.FileSystem == "URL" {
		if location, err := url.Parse(name); err == nil {
			name = location.Path
		}
	}
	return strings.TrimPrefix(path.Ext(name), ".")
}

// SoundWAV 封装PDF采样音频，保持采样率、声道及数值，超过RIFF容量时使用RF64
// 入参: ctx 取消上下文, sound 音频流及参数
// 返回: []byte WAVE文件, error 不可表示的参数或损坏的采样数据
func SoundWAV(ctx context.Context, sound pdfgo.Sound) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sound.File != nil || sound.Compression != "" {
		return nil, &pdfgo.UnsupportedError{Feature: "external or compressed sound samples"}
	}
	if sound.Stream == nil || sound.Channels <= 0 || math.IsNaN(sound.Rate) || math.IsInf(sound.Rate, 0) || sound.Rate <= 0 {
		return nil, fmt.Errorf("invalid sound sample parameters")
	}
	if sound.Rate != math.Trunc(sound.Rate) || sound.Rate > math.MaxUint32 || sound.Channels > math.MaxUint16 || sound.Bits != 8 && sound.Bits != 16 && sound.Bits != 24 && sound.Bits != 32 {
		return nil, &pdfgo.UnsupportedError{Feature: "sound parameters in WAVE"}
	}
	format := uint16(1)
	if sound.Encoding == "muLaw" {
		format = 7
	} else if sound.Encoding == "ALaw" {
		format = 6
	}
	if format != 1 && sound.Bits != 8 {
		return nil, fmt.Errorf("invalid companded sound depth")
	}
	width := sound.Bits / 8
	align := uint64(sound.Channels) * uint64(width)
	rate := uint64(sound.Rate)
	if align > math.MaxUint16 || rate*align > math.MaxUint32 {
		return nil, &pdfgo.UnsupportedError{Feature: "sound block alignment in WAVE"}
	}
	data, err := sound.Stream.DecodeContext(ctx)
	if err != nil {
		return nil, err
	}
	if uint64(len(data))%align != 0 {
		return nil, fmt.Errorf("incomplete sound sample frame")
	}
	if format == 1 {
		for offset := 0; offset < len(data); offset += width {
			if offset%(1<<16) == 0 {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
			}
			if sound.Encoding == "Raw" && width > 1 || sound.Encoding == "Signed" && width == 1 {
				data[offset] ^= 0x80
			}
			for left, right := offset, offset+width-1; left < right; left, right = left+1, right-1 {
				data[left], data[right] = data[right], data[left]
			}
		}
	}
	formatSize := uint32(16)
	extra := uint64(0)
	if format == 1 && (sound.Bits > 16 || sound.Channels > 2) {
		format = 0xfffe
		formatSize = 40
		extra = 36
	} else if format != 1 {
		formatSize = 18
		extra = 14
	}
	size := uint64(36) + extra + uint64(len(data)) + uint64(len(data)%2)
	large := size > math.MaxUint32
	var out bytes.Buffer
	put16 := func(v uint16) { _ = binary.Write(&out, binary.LittleEndian, v) }
	put32 := func(v uint32) { _ = binary.Write(&out, binary.LittleEndian, v) }
	put64 := func(v uint64) { _ = binary.Write(&out, binary.LittleEndian, v) }
	if large {
		out.WriteString("RF64")
		put32(math.MaxUint32)
		out.WriteString("WAVEds64")
		put32(28)
		put64(size + 36)
		put64(uint64(len(data)))
		put64(uint64(len(data)) / align)
		put32(0)
	} else {
		out.WriteString("RIFF")
		put32(uint32(size))
		out.WriteString("WAVE")
	}
	out.WriteString("fmt ")
	put32(formatSize)
	put16(format)
	put16(uint16(sound.Channels))
	put32(uint32(rate))
	put32(uint32(rate * align))
	put16(uint16(align))
	put16(uint16(sound.Bits))
	if format != 1 {
		if format == 0xfffe {
			put16(22)
			put16(uint16(sound.Bits))
			put32(0)
			out.Write([]byte{1, 0, 0, 0, 0, 0, 0x10, 0, 0x80, 0, 0, 0xaa, 0, 0x38, 0x9b, 0x71})
		} else {
			put16(0)
		}
		out.WriteString("fact")
		put32(4)
		if large {
			put32(math.MaxUint32)
		} else {
			put32(uint32(uint64(len(data)) / align))
		}
	}
	out.WriteString("data")
	if large {
		put32(math.MaxUint32)
	} else {
		put32(uint32(len(data)))
	}
	out.Write(data)
	if len(data)%2 != 0 {
		out.WriteByte(0)
	}
	return out.Bytes(), ctx.Err()
}
