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
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

// replaceFileProc 保留目标文件属性并替换文件内容
var replaceFileProc = syscall.NewLazyDLL("kernel32.dll").NewProc("ReplaceFileW")

// memoryStatusProc 查询系统物理内存和提交额度
var memoryStatusProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")

// openReadFile 允许原子替换路径，同时保持已有读取句柄有效
func openReadFile(name string) (*os.File, error) {
	path, err := nativeFilePath(name)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	handle, err := syscall.CreateFile(path, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(handle), name), nil
}

// replaceFile 替换已有文件，目标不存在时直接重命名
func replaceFile(source, target string) error {
	from, err := nativeFilePath(source)
	if err != nil {
		return err
	}
	to, err := nativeFilePath(target)
	if err != nil {
		return err
	}
	result, _, err := replaceFileProc.Call(uintptr(unsafe.Pointer(to)), uintptr(unsafe.Pointer(from)), 0, 0, 0, 0)
	if result != 0 {
		return nil
	}
	if err == syscall.ERROR_FILE_NOT_FOUND {
		return os.Rename(source, target)
	}
	return &os.LinkError{Op: "replace", Old: source, New: target, Err: err}
}

// nativeFilePath 转换绝对路径并支持长路径与网络共享
func nativeFilePath(name string) (*uint16, error) {
	path, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, `\\?\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	return syscall.UTF16PtrFromString(path)
}

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
