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

package image

// NormalizeFormat 标准化印章媒体类型
// 入参: s 原始媒体类型
// 返回: string 标准媒体类型
func NormalizeFormat(s string) string {
	switch s {
	case "jpg":
		return "jpeg"
	case "bmp":
		return "bmp"
	case "jb2", "gbig2":
		return "jbig2"
	default:
		return s
	}
}
