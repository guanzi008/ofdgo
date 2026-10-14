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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"

	"github.com/xiaoqidun/pdfgo"
)

// ClipPathKey 按路径完整内容与变换生成键，不依赖可变切片的地址
// 入参: ctx 取消上下文, path 裁剪路径, matrix 页面变换
// 返回: [32]byte 内容摘要, error 取消错误
func ClipPathKey(ctx context.Context, path pdfgo.Path, matrix pdfgo.Matrix) ([32]byte, error) {
	hash := sha256.New()
	var value [16]byte
	for _, number := range matrix {
		binary.LittleEndian.PutUint64(value[:8], math.Float64bits(number))
		hash.Write(value[:8])
	}
	binary.LittleEndian.PutUint64(value[:8], uint64(len(path.Segments)))
	value[8] = 0
	if path.EvenOdd {
		value[8] = 1
	}
	hash.Write(value[:9])
	for _, segment := range path.Segments {
		if err := ctx.Err(); err != nil {
			return [32]byte{}, err
		}
		binary.LittleEndian.PutUint64(value[:8], uint64(len(segment.Operator)))
		binary.LittleEndian.PutUint64(value[8:], uint64(len(segment.Points)))
		hash.Write(value[:])
		hash.Write([]byte(segment.Operator))
		for index, point := range segment.Points {
			if index&255 == 0 {
				if err := ctx.Err(); err != nil {
					return [32]byte{}, err
				}
			}
			binary.LittleEndian.PutUint64(value[:8], math.Float64bits(point.X))
			binary.LittleEndian.PutUint64(value[8:], math.Float64bits(point.Y))
			hash.Write(value[:])
		}
	}
	if err := ctx.Err(); err != nil {
		return [32]byte{}, err
	}
	var key [32]byte
	hash.Sum(key[:0])
	return key, nil
}
