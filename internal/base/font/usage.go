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

package font

// Usage 指定子集需要保留的字符、字形及引用是否存在不确定性
type Usage struct {
	Characters map[rune]bool
	Glyphs     map[uint16]bool
	Unsafe     bool
}

// newUsage 创建具有可写字符和字形集合的子集输入
// 返回: *Usage 用字信息
func newUsage() *Usage {
	return &Usage{Characters: make(map[rune]bool), Glyphs: make(map[uint16]bool)}
}
