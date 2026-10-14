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
	"github.com/xiaoqidun/pdfgo"
)

// OutputColor 将合成结果映射到OFD输出，与保留的设备四色对象使用相同显示换算
// 入参: space 合成空间, values 非预乘分量
// 返回: [3]float64 显示颜色, error 校准颜色转换错误
func OutputColor(space *pdfgo.ColorSpace, values [4]float64) ([3]float64, error) {
	if space.Model == "DeviceCMYK" && !space.Calibrated() {
		black := 1 - values[3]
		return [3]float64{(1 - values[0]) * black, (1 - values[1]) * black, (1 - values[2]) * black}, nil
	}
	return space.RGB(values[:space.Components()], "RelativeColorimetric")
}

// NormalBlend 判断混合模式是否为普通覆盖
// 入参: mode 混合模式
// 返回: bool 是否普通覆盖
func NormalBlend(mode pdfgo.Name) bool {
	return mode == "" || mode == "Normal" || mode == "Compatible"
}
