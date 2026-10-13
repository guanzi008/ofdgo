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

package geom

import (
	"math"
)

// finite 判断数值是否有限
// 入参: value 数值
// 返回: bool 是否有限
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// positive 判断正有限数
// 入参: v 待检查数值
// 返回: bool 是否为正有限数
func positive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }
