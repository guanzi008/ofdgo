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
	"strings"
	"time"

	"github.com/xiaoqidun/pdfgo"
)

// MetadataDate 保留当地日历日期，PDF省略时区按GMT，XMP省略日或时区时不推算
// 入参: text 来源日期, xmp 是否为XMP日期
// 返回: time.Time 解析值, bool 是否精确到日, bool 是否可比较绝对时间, error 日期错误
func MetadataDate(text string, xmp bool) (time.Time, bool, bool, error) {
	if text == "" {
		return time.Time{}, false, false, nil
	}
	if !xmp {
		date, err := pdfgo.ParseDate(text)
		return date, err == nil && date.Year() > 0, err == nil, err
	}
	if !strings.Contains(text, ",") {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00", "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02", "2006-01", "2006"} {
			if date, err := time.Parse(layout, text); err == nil {
				zoned := strings.Contains(layout, "Z07:00")
				if zoned && !strings.HasSuffix(text, "Z") {
					zone := text[len(text)-6:]
					if zone[1:3] > "23" || zone[4:] > "59" {
						return time.Time{}, false, false, fmt.Errorf("invalid XMP time zone")
					}
				}
				return date, len(text) >= 10 && date.Year() > 0, zoned, nil
			}
		}
	}
	return time.Time{}, false, false, fmt.Errorf("invalid XMP date")
}
