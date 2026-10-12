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
	"math"
	"strconv"
	"strings"
)

// textLineScanner 顺序判断文本段换行，复用继承坐标和字符到字形的变换
type textLineScanner struct {
	size       float64
	enabled    bool
	started    bool
	firstX     string
	previous   TextCode
	offset     int
	pending    []CGTransform
	transforms map[int][2]int
}

// newTextLineScanner 创建独立的换行扫描器，不修改文字对象
// 入参: obj 定位文字
// 返回: textLineScanner 扫描状态
func newTextLineScanner(obj TextObject) textLineScanner {
	return textLineScanner{size: obj.Size, enabled: len(obj.TextCode) > 1 && obj.ReadDirection == 0 && obj.CharDirection == 0, previous: TextCode{X: "0", Y: "0"}, pending: obj.CGTransform}
}

// glyphCount 首次需要字形范围时解析变换，后续文本段复用结果
// 入参: code 文本段, offset 对象内字符起点
// 返回: int 字形数量
func (s *textLineScanner) glyphCount(code TextCode, offset int) int {
	if len(s.pending) != 0 {
		s.transforms = make(map[int][2]int, len(s.pending))
	}
	for _, transform := range s.pending {
		if transform.CodePosition < 0 {
			continue
		}
		glyphs := len(parseInts(transform.Glyphs))
		if transform.GlyphCount > 0 {
			glyphs = min(glyphs, transform.GlyphCount)
		}
		if glyphs > 0 {
			s.transforms[transform.CodePosition] = [2]int{max(1, transform.CodeCount), glyphs}
		}
	}
	s.pending = nil
	return textCodeGlyphCount(code, offset, s.transforms)
}

// next 继承文本段原点并根据实际字形范围判断是否换行
// 入参: code 按文档顺序传入的文本段
// 返回: bool 当前段前是否换行
func (s *textLineScanner) next(code TextCode) bool {
	if !s.enabled {
		return false
	}
	previous, offset := s.previous, s.offset
	if s.started && (len(s.pending) != 0 || len(s.transforms) != 0) {
		s.offset += textCodeRuneCount(previous.Value)
	}
	if code.X == "" {
		code.X = previous.X
	}
	if code.Y == "" {
		code.Y = previous.Y
	}
	s.previous = code
	if !s.started {
		s.started, s.firstX = true, code.X
		return false
	}
	x, ex := strconv.ParseFloat(code.X, 64)
	start, es := strconv.ParseFloat(s.firstX, 64)
	y, ey := strconv.ParseFloat(code.Y, 64)
	prev, ep := strconv.ParseFloat(previous.Y, 64)
	if ex != nil || es != nil || ey != nil || ep != nil {
		return false
	}
	if x == start && y != prev {
		return true
	}
	left, err := strconv.ParseFloat(previous.X, 64)
	if err != nil || s.size <= 0 || y-prev < s.size/2 || previous.DeltaY != "" || code.DeltaY != "" {
		return false
	}
	count := s.glyphCount(previous, offset)
	if count == 0 {
		return false
	}
	right := left + s.size
	positioner := NewTextPositioner(previous)
	for range count {
		right = math.Max(right, positioner.Next().X+s.size)
	}
	return x <= right
}

// textCodeGlyphCount 按文本段的字符起点统计实际字形数量
// 入参: code 文本段, offset 对象内字符起点, transforms 已解析的字符到字形变换
// 返回: int 字形数量
func textCodeGlyphCount(code TextCode, offset int, transforms map[int][2]int) int {
	if code.Index != "" {
		count := 0
		for _, field := range strings.Fields(code.Index) {
			first, last, interval := strings.Cut(field, "-")
			start, err := strconv.ParseUint(first, 10, 16)
			if err != nil {
				continue
			}
			if !interval {
				count++
				continue
			}
			end, err := strconv.ParseUint(last, 10, 16)
			if err == nil && end >= start {
				count += int(end-start) + 1
			}
		}
		return count
	}
	count := textCodeRuneCount(code.Value)
	if len(transforms) == 0 || count == 0 {
		return count
	}
	glyphs := 0
	for i := 0; i < count; {
		if transform, ok := transforms[offset+i]; ok && transform[0] <= count-i {
			i += transform[0]
			glyphs += transform[1]
		} else {
			i++
			glyphs++
		}
	}
	return glyphs
}
