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
	"fmt"

	"github.com/xiaoqidun/pdfgo"
)

// RenditionFloatingWindow 校验浮窗必需的像素尺寸，呈现偏好不写成视频固有尺寸
// 入参: reader PDF阅读器, screen 媒体屏幕参数
// 返回: bool 是否具备合法尺寸, error 引用错误
func RenditionFloatingWindow(reader *pdfgo.Reader, screen pdfgo.Dictionary) (bool, error) {
	value, err := reader.Resolve(screen["BE"])
	if err != nil {
		return false, err
	}
	preferences, _ := value.(pdfgo.Dictionary)
	value, err = reader.Resolve(preferences["F"])
	if err != nil {
		return false, err
	}
	window, ok := value.(pdfgo.Dictionary)
	if !ok {
		return false, nil
	}
	value, err = reader.Resolve(window["D"])
	if err != nil {
		return false, err
	}
	size, ok := value.(pdfgo.Array)
	if !ok || len(size) != 2 {
		return false, nil
	}
	for _, part := range size {
		value, err = reader.Resolve(part)
		if err != nil {
			return false, err
		}
		number, ok := value.(pdfgo.Integer)
		if !ok || number < 0 {
			return false, nil
		}
	}
	return true, nil
}

// RenditionCriteria 检查媒体环境条件，未知尽力处理字段不影响候选可用性
// 入参: reader PDF阅读器, rendition 媒体呈现或选择器
// 返回: bool 是否可用, error 条件字典或引用错误
func RenditionCriteria(reader *pdfgo.Reader, rendition *pdfgo.Rendition) (bool, error) {
	for _, constraints := range []struct {
		dict     pdfgo.Dictionary
		required bool
	}{{rendition.MustHonor, true}, {rendition.BestEffort, false}} {
		for key, object := range constraints.dict {
			if key != "C" {
				if constraints.required {
					return false, nil
				}
				continue
			}
			value, err := reader.Resolve(object)
			if err != nil {
				return false, err
			}
			criteria, ok := value.(pdfgo.Dictionary)
			if !ok {
				return false, fmt.Errorf("invalid media criteria")
			}
			for name, object := range criteria {
				value, err := reader.Resolve(object)
				if err != nil {
					return false, err
				}
				if name == "Type" && value == pdfgo.Name("MediaCriteria") {
					continue
				}
				switch name {
				case "Type", "A", "C", "O", "S", "R", "D", "Z", "V", "P", "L":
					return false, nil
				default:
					if constraints.required {
						return false, nil
					}
				}
			}
		}
	}
	return true, nil
}

// RenditionParameters 合并可等价表达的播放参数，不支持的偏好不阻断播放
// 入参: reader PDF阅读器, dict 参数字典, supported 字段及可表达值的检查
// 返回: pdfgo.Dictionary 合并后的值, bool 是否可用, error 字典或引用错误
func RenditionParameters(reader *pdfgo.Reader, dict pdfgo.Dictionary, supported map[pdfgo.Name]func(pdfgo.Object) bool) (pdfgo.Dictionary, bool, error) {
	values := make(pdfgo.Dictionary)
	for key := range dict {
		if key != "Type" && key != "MH" && key != "BE" {
			return nil, false, nil
		}
	}
	value, err := reader.Resolve(dict["MH"])
	if err != nil {
		return nil, false, err
	}
	mandatory, _ := value.(pdfgo.Dictionary)
	for _, key := range []pdfgo.Name{"BE", "MH"} {
		value, err := reader.Resolve(dict[key])
		if err != nil {
			return nil, false, err
		}
		if value == nil {
			continue
		}
		parameters, ok := value.(pdfgo.Dictionary)
		if !ok {
			return nil, false, fmt.Errorf("invalid media playback constraints")
		}
		for name, object := range parameters {
			if _, overridden := mandatory[name]; key == "BE" && overridden {
				continue
			}
			check := supported[name]
			if check == nil {
				if key == "MH" {
					return nil, false, nil
				}
				continue
			}
			resolved, err := reader.Resolve(object)
			if err != nil {
				return nil, false, err
			}
			if !check(resolved) {
				if key == "MH" {
					return nil, false, nil
				}
				continue
			}
			values[name] = resolved
		}
	}
	return values, true, nil
}
