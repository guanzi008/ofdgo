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

package webview

import (
	"syscall"
	"unsafe"
)

// memoryStatusProc 查询系统物理内存和提交额度
var memoryStatusProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// fontNameLanguage 获取系统显示语言对应的OpenType名称表标识
// 返回: uint16 名称表平台标识, uint16 系统显示语言标识
func fontNameLanguage() (uint16, uint16) {
	language, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage").Call()
	return 3, uint16(language)
}

// conversionMemory 预留系统内存后返回可用于新增转换的额度
// 返回: int64 可用字节数，查询失败时暂停扩容
func conversionMemory() int64 {
	status := struct {
		length           uint32
		load             uint32
		total            uint64
		available        uint64
		totalPage        uint64
		availablePage    uint64
		totalVirtual     uint64
		availableVirtual uint64
		extended         uint64
	}{}
	status.length = uint32(unsafe.Sizeof(status))
	ok, _, _ := memoryStatusProc.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return 0
	}
	return max(0, int64(min(status.available, status.availablePage))-int64(status.total/8))
}
