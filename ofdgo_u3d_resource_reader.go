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
	"fmt"
	"math"
	"unicode/utf8"
)

// u3dResourceReader 读取骨骼权重与动作资源中交错的普通字段和压缩字段
type u3dResourceReader struct {
	values  *u3dValues
	decoder *u3dDecoder
	bits    *u3dBits
	hist    [8]u3dHistogram
	charged uint64
}

// value 读取指定上下文的压缩字段，首次使用时初始化算术区间
// 入参: context 压缩上下文, small 是否为8位字段
// 返回: uint32 字段值
func (r *u3dResourceReader) value(context int, small bool) uint32 {
	if r.err() != nil {
		return 0
	}
	if r.decoder.plain {
		if small {
			return uint32(r.values.u8())
		}
		return r.values.u32()
	}
	if r.bits == nil {
		r.bits = newU3DBits(r.values.data[r.values.pos:])
		r.bits.reserve = func(count, size uint64) error {
			if err := r.decoder.reserve(count, size); err != nil {
				return err
			}
			r.charged += count * size
			return nil
		}
	}
	return r.bits.dynamicValue(&r.hist[context], small)
}

// u32 读取普通32位字段，压缩开始后沿用当前算术区间
// 返回: uint32 字段值
func (r *u3dResourceReader) u32() uint32 {
	if r.bits != nil {
		return r.bits.u32()
	}
	return r.values.u32()
}

// f32 读取有限单精度数值
// 返回: float32 字段值
func (r *u3dResourceReader) f32() float32 {
	value := math.Float32frombits(r.u32())
	if !finite(float64(value)) && r.err() == nil {
		r.values.err = fmt.Errorf("invalid U3D floating-point value")
	}
	return value
}

// text 读取UTF-8名称，压缩开始后逐字节解码而不重置区间
// 返回: string 名称
func (r *u3dResourceReader) text() string {
	if r.err() != nil {
		return ""
	}
	if r.bits == nil {
		return r.values.text()
	}
	length := int(r.bits.byteValue()) | int(r.bits.byteValue())<<8
	if r.err() != nil {
		return ""
	}
	if err := r.decoder.reserve(uint64(length), 2); err != nil {
		r.values.err = err
		return ""
	}
	data := make([]byte, length)
	for index := range data {
		if index%1024 == 0 {
			if err := r.decoder.ctx.Err(); err != nil {
				r.values.err = err
				return ""
			}
		}
		data[index] = r.bits.byteValue()
	}
	if r.err() == nil && !utf8.Valid(data) {
		r.values.err = fmt.Errorf("invalid U3D UTF-8 string")
	}
	return string(data)
}

// err 返回字段或算术解码错误
// 返回: error 读取错误
func (r *u3dResourceReader) err() error {
	if r.values.err != nil {
		return r.values.err
	}
	if r.bits != nil {
		return r.bits.err
	}
	return nil
}

// done 校验末尾冲刷字段和块边界
// 返回: error 读取或多余数据错误
func (r *u3dResourceReader) done() error {
	if err := r.err(); err != nil {
		return err
	}
	if r.bits != nil {
		if err := r.bits.finish(); err != nil {
			return err
		}
		r.values.pos = len(r.values.data)
	}
	return r.values.done()
}

// release 归还当前块压缩上下文占用的临时预算
func (r *u3dResourceReader) release() {
	r.decoder.remaining += r.charged
	r.charged = 0
}
