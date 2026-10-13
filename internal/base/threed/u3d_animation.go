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
	"context"
	"fmt"
	"io"
)

// U3DAnimation 保存ECMA-363第9.7.3节的动画修饰器，Index为链内位置
// Attributes的1、2、4、8分别表示启动、锁定根骨骼、单轨播放和动作间平滑过渡
// TimeScale为整体时间倍率，BlendTime以毫秒计；Motions为空时使用无轨道的默认动作
type U3DAnimation struct {
	Index, Attributes    uint32
	TimeScale, BlendTime float32
	Motions              []U3DAnimationMotion
}

// U3DAnimationMotion 保存动画中的动作引用，空名称引用默认动作
// Attributes的1、2分别表示循环和同步结束，TimeOffset以毫秒计，TimeScale为动作时间倍率
type U3DAnimationMotion struct {
	Name                  string
	Attributes            uint32
	TimeOffset, TimeScale float32
}

// animation 读取动画参数及有序动作引用，不在解码时调度或求值
// 入参: r 字段读取器, name 所属链名, chain 链类型, index 链内位置
// 返回: error 结构、参数、预算或取消错误
func (d *u3dDecoder) animation(r *u3dValues, name string, chain int, index uint32) error {
	animation := U3DAnimation{Index: index, Attributes: r.u32(), TimeScale: r.f32()}
	count := r.u32()
	if r.err != nil {
		return r.err
	}
	if len(r.data)-r.pos < 4 || uint64(count) > uint64(len(r.data)-r.pos-4)/14 {
		return io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 32); err != nil {
		return err
	}
	if count != 0 {
		animation.Motions = make([]U3DAnimationMotion, int(count))
	}
	for i := range animation.Motions {
		if i%256 == 0 {
			if err := d.ctx.Err(); err != nil {
				return err
			}
		}
		motion := &animation.Motions[i]
		motion.Name, motion.Attributes = r.text(), r.u32()
		motion.TimeOffset, motion.TimeScale = r.f32(), r.f32()
		if r.err != nil {
			return r.err
		}
	}
	animation.BlendTime = r.f32()
	if err := r.done(); err != nil {
		return err
	}
	if err := animation.validate(d.ctx); err != nil {
		return err
	}
	if chain == 1 {
		mesh := &d.meshes[name].mesh
		mesh.Animations = append(mesh.Animations, animation)
	} else {
		node := &d.model.Nodes[d.nodes[name]]
		node.Animations = append(node.Animations, animation)
	}
	return nil
}

// validate 校验动画属性及有限数值，保留零倍率与反向播放倍率
// 入参: ctx 取消上下文
// 返回: error 参数或取消错误
func (animation U3DAnimation) validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if animation.Index == 0 || animation.Attributes & ^uint32(15) != 0 || !finite(float64(animation.TimeScale)) || !finite(float64(animation.BlendTime)) || animation.BlendTime < 0 {
		return fmt.Errorf("invalid U3D animation parameters")
	}
	for i, motion := range animation.Motions {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if motion.Attributes & ^uint32(3) != 0 || !finite(float64(motion.TimeOffset)) || !finite(float64(motion.TimeScale)) {
			return fmt.Errorf("invalid U3D animation motion parameters")
		}
	}
	return nil
}

// checkAnimations 校验全部动作引用，仅对接入场景的修饰器检查求值能力
// 返回: error 引用、能力或取消错误
func (r *Renderer) checkAnimations() error {
	check := func(animations []U3DAnimation, evaluate bool) error {
		for _, animation := range animations {
			if err := animation.validate(r.ctx); err != nil {
				return err
			}
			for i, motion := range animation.Motions {
				if i%256 == 0 {
					if err := r.ctx.Err(); err != nil {
						return err
					}
				}
				if motion.Name == "" {
					continue
				}
				index, exists := r.motionIndex[motion.Name]
				if !exists {
					return fmt.Errorf("missing U3D animation motion %q", motion.Name)
				}
				if evaluate && len(animation.Motions) > 1 && len(r.model.Motions[index].Tracks) != 0 {
					return fmt.Errorf("unsupported U3D animation evaluation for motion %q", motion.Name)
				}
			}
		}
		return nil
	}
	var active []bool
	if r.connected != nil {
		if err := r.Reserve(uint64(len(r.model.Meshes)), 1); err != nil {
			return err
		}
		defer func() { r.remaining += uint64(len(active)) }()
		active = make([]bool, len(r.model.Meshes))
		for i, node := range r.model.Nodes {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return err
				}
			}
			if r.connected[i] && node.Kind == "Model" {
				if mesh, exists := r.meshes[node.Resource]; exists {
					active[mesh] = true
				}
			}
		}
	}
	for i := range r.model.Meshes {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if err := check(r.model.Meshes[i].Animations, active == nil || active[i]); err != nil {
			return err
		}
	}
	for i := range r.model.Nodes {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if err := check(r.model.Nodes[i].Animations, r.connected == nil || r.connected[i]); err != nil {
			return err
		}
	}
	return r.ctx.Err()
}
