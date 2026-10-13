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

package threed

import (
	"fmt"
)

// U3DNodeState 按名称覆盖节点，未指定透明度或可见性时沿父路径继承
// Opacity范围为0到1，采用标准透明混合；Matrix为相对父节点的列式仿射矩阵
// 显式Opacity覆盖材质和纹理合成的透明度，之后仍应用视图附加透明度
// 未匹配的名称不影响场景，Matrix和Motion均为空时保留每条父路径的原始变换
// Motion以指定轨道替换节点变换，与Matrix互斥，不驱动骨架；显式变换优先于动画修饰器
// Bones覆盖动画求值后的指定骨骼姿态，未指定项沿用当前局部变换，不修改共享几何
type U3DNodeState struct {
	Name    string
	Opacity *float64
	Visible *bool
	Matrix  *[16]float64
	Motion  *U3DTrackState
	Bones   []U3DBoneState
}

// nodeStates 建立有预算限制的覆盖索引，重复名称整体替换而不合并字段
// 入参: groups 按优先级递增排列的节点覆盖分组
// 返回: error 数值、预算或取消错误
func (r *Renderer) nodeStates(groups ...[]U3DNodeState) error {
	var count uint64
	for _, states := range groups {
		if uint64(len(states)) > ^uint64(0)-count {
			return fmt.Errorf("U3D node state count exceeds limit")
		}
		count += uint64(len(states))
	}
	if count == 0 {
		return nil
	}
	if err := r.Reserve(count, 160); err != nil {
		return err
	}
	r.states = make(map[string]U3DNodeState, int(count))
	for _, states := range groups {
		for i, state := range states {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			r.states[state.Name] = state
		}
	}
	var samples map[U3DTrackState]*[16]float64
	defer func() {
		if samples != nil {
			r.remaining += 1024 + uint64(len(samples))*192
		}
	}()
	for name, state := range r.states {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if state.Opacity != nil && (!finite(*state.Opacity) || *state.Opacity < 0 || *state.Opacity > 1) {
			return fmt.Errorf("invalid U3D node opacity")
		}
		if state.Matrix != nil && !u3dMatrix(*state.Matrix).valid() {
			return fmt.Errorf("invalid U3D node matrix")
		}
		if state.Motion != nil {
			if state.Matrix != nil || !finite(state.Motion.Time) {
				return fmt.Errorf("invalid U3D node motion state")
			}
			if samples == nil {
				if err := r.Reserve(1, 1024); err != nil {
					return err
				}
				samples = make(map[U3DTrackState]*[16]float64)
			}
			matrix := samples[*state.Motion]
			if matrix == nil {
				if err := r.Reserve(1, 320); err != nil {
					return err
				}
				value, err := r.trackMatrix(*state.Motion)
				if err != nil {
					return err
				}
				matrix = &value
				samples[*state.Motion] = matrix
			}
			state.Matrix = matrix
			r.states[name] = state
		}
	}
	return nil
}

// modelVisibility 在节点覆盖生效时恢复隐藏模型，保留原有正反面选择
// 入参: node 模型节点
// 返回: uint32 可见面标志
func (instance u3dInstance) modelVisibility(node *U3DNode) uint32 {
	if instance.visibility == 0 {
		return 0
	}
	if instance.visibility == 1 && node.Visibility == 0 {
		return 3
	}
	return node.Visibility
}
