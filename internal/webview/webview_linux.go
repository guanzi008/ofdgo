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
)

// conversionMemory 未提供系统内存探测时仅按并行数调度
var conversionMemory func() int64

// instanceDirectory 解析数据目录的实际路径，使符号链接复用同一实例
// 入参: path 数据目录路径
// 返回: string 规范路径, error 路径解析错误
func instanceDirectory(path string) (string, error) {
	return filepath.EvalSymlinks(path)
}

// fontNameLanguage 获取Linux区域设置对应的OpenType名称表标识
// 返回: uint16 名称表平台标识, uint16 显示语言标识
func fontNameLanguage() (uint16, uint16) {
	locale := ""
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if locale = os.Getenv(name); locale != "" {
			break
		}
	}
	locale = strings.ToLower(strings.ReplaceAll(locale, "-", "_"))
	locale, _, _ = strings.Cut(locale, ".")
	locale, _, _ = strings.Cut(locale, "@")
	switch locale {
	case "zh", "zh_cn", "zh_hans":
		return 3, 0x0804
	case "zh_hk":
		return 3, 0x0c04
	case "zh_mo":
		return 3, 0x1404
	case "zh_tw", "zh_hant":
		return 3, 0x0404
	case "zh_sg":
		return 3, 0x1004
	case "ja", "ja_jp":
		return 3, 0x0411
	case "ko", "ko_kr":
		return 3, 0x0412
	case "c", "posix", "en", "en_us":
		return 3, 0x0409
	}
	return 0xffff, 0xffff
}
