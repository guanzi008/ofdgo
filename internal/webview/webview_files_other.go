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

//go:build !windows

package webview

import "os"

// openReadFile 打开独立读取句柄
func openReadFile(name string) (*os.File, error) {
	return os.Open(name)
}

// replaceFile 替换目标文件并保留已有读取句柄
func replaceFile(source, target string) error {
	return os.Rename(source, target)
}
