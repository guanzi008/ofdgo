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

//go:build !windows && !linux

package webview

import "path/filepath"

// conversionMemory 未提供系统内存探测时仅按并行数调度
var conversionMemory func() int64

// instanceDirectory 解析数据目录的实际路径
// 入参: path 数据目录路径
// 返回: string 规范路径, error 路径解析错误
func instanceDirectory(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// fontNameLanguage 未指定系统显示语言时沿用字体名称表顺序
// 返回: uint16 未指定平台标识, uint16 未指定语言标识
func fontNameLanguage() (uint16, uint16) {
	return 0xffff, 0xffff
}
