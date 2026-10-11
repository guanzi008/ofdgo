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

package ofdgo

import (
	"cmp"
	"fmt"
	"math"
	"slices"
)

// u3dAnimationState 保存一次绘制中动画求值的节点变换、骨骼姿态及对应权重
type u3dAnimationState struct {
	matrix  u3dMatrix
	bones   []U3DBoneState
	weights *U3DBoneWeights
}

// u3dPlaybackKey 标识一条轨道在指定动作时间的采样，不包含节点绑定
type u3dPlaybackKey struct {
	track *U3DMotionTrack
	time  float64
}

// u3dPlaybackCache 在一次动画求值中共享轨道时间范围和采样结果
type u3dPlaybackCache struct {
	render  *u3dRender
	bounds  map[*U3DMotionTrack][2]float64
	samples map[u3dPlaybackKey]U3DKeyFrame
	bytes   uint64
}

// limits 校验轨道时间并缓存范围，避免共享动作按节点数量重复扫描全部帧
// 入参: track 动作轨道
// 返回: [2]float64 首末时间, error 空轨道、数值、预算或取消错误
func (c *u3dPlaybackCache) limits(track *U3DMotionTrack) ([2]float64, error) {
	if err := c.render.ctx.Err(); err != nil {
		return [2]float64{}, err
	}
	if bounds, exists := c.bounds[track]; exists {
		return bounds, nil
	}
	if len(track.Frames) == 0 {
		return [2]float64{}, fmt.Errorf("empty U3D animation track")
	}
	bounds := [2]float64{math.Inf(1), math.Inf(-1)}
	for i, frame := range track.Frames {
		if i%256 == 0 {
			if err := c.render.ctx.Err(); err != nil {
				return bounds, err
			}
		}
		if !finite(float64(frame.Time)) {
			return bounds, fmt.Errorf("invalid U3D animation key frame time")
		}
		bounds[0], bounds[1] = min(bounds[0], float64(frame.Time)), max(bounds[1], float64(frame.Time))
	}
	if c.bounds == nil {
		if err := c.render.reserve(1, 1024); err != nil {
			return bounds, err
		}
		c.bytes += 1024
		c.bounds = make(map[*U3DMotionTrack][2]float64)
	}
	if err := c.render.reserve(1, 96); err != nil {
		return bounds, err
	}
	c.bytes += 96
	c.bounds[track] = bounds
	return bounds, nil
}

// sample 复用相同轨道和时间的姿态，缓存只在本次求值期间有效
// 入参: track 动作轨道, time 已换算的动作时间
// 返回: U3DKeyFrame 采样姿态, error 轨道、预算或取消错误
func (c *u3dPlaybackCache) sample(track *U3DMotionTrack, time float64) (U3DKeyFrame, error) {
	if err := c.render.ctx.Err(); err != nil {
		return U3DKeyFrame{}, err
	}
	key := u3dPlaybackKey{track, time}
	if frame, exists := c.samples[key]; exists {
		return frame, nil
	}
	frame, err := track.Sample(c.render.ctx, time, false)
	if err != nil {
		return frame, err
	}
	if c.samples == nil {
		if err := c.render.reserve(1, 1024); err != nil {
			return frame, err
		}
		c.bytes += 1024
		c.samples = make(map[u3dPlaybackKey]U3DKeyFrame)
	}
	if err := c.render.reserve(1, 128); err != nil {
		return frame, err
	}
	c.bytes += 128
	c.samples[key] = frame
	return frame, nil
}

// prepareAnimations 按资源链、节点链顺序求值，不保存上一帧状态或推进实际时钟
// 入参: time 从初始时刻起算的秒数
// 返回: error 动作、骨架、修饰顺序、预算或取消错误
func (r *u3dRender) prepareAnimations(time float64) error {
	cache := u3dPlaybackCache{render: r}
	defer func() { r.remaining += cache.bytes }()
	for i := range r.model.Nodes {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.connected != nil && !r.connected[i] {
			continue
		}
		node := &r.model.Nodes[i]
		mesh := r.nodeMesh(node)
		groups := [2][]U3DAnimation{nil, node.Animations}
		if node.Kind == "Model" && mesh != nil {
			groups[0] = mesh.Animations
		}
		state := u3dAnimationState{matrix: u3dIdentity()}
		used := false
		for chain, group := range groups {
			ordered := group
			if len(group) > 1 {
				if err := r.reserve(uint64(len(group)), 48); err != nil {
					return err
				}
				ordered = slices.Clone(group)
				slices.SortFunc(ordered, func(a, b U3DAnimation) int { return cmp.Compare(a.Index, b.Index) })
			}
			for index, animation := range ordered {
				if err := r.ctx.Err(); err != nil {
					return err
				}
				if index != 0 && animation.Index == ordered[index-1].Index {
					return fmt.Errorf("duplicate U3D animation chain index")
				}
				if len(animation.Motions) == 0 || animation.Motions[0].Name == "" {
					continue
				}
				motion := &r.model.Motions[r.motionIndex[animation.Motions[0].Name]]
				if len(motion.Tracks) == 0 {
					continue
				}
				used = true
				if err := r.sampleAnimation(&state, node, mesh, &animation, motion, chain, time, &cache); err != nil {
					return err
				}
			}
			if len(group) > 1 {
				r.remaining += uint64(len(group)) * 48
			}
		}
		if used {
			if r.animated == nil {
				if err := r.reserve(1, 1024); err != nil {
					return err
				}
				r.animated = make(map[string]u3dAnimationState)
			}
			if err := r.reserve(1, 384); err != nil {
				return err
			}
			r.animated[node.Name] = state
		}
	}
	return r.ctx.Err()
}

// animationTime 将毫秒偏移和两级时间倍率换算为动作时间，已绑定轨道共用循环区间
// 入参: animation 动画修饰器, tracks 动作轨道, bindings 骨骼绑定，空值表示全部轨道, time 从初始时刻起算的秒数, cache 本次采样缓存
// 返回: float64 动作采样时间, error 时间数值或取消错误
func (r *u3dRender) animationTime(animation *U3DAnimation, tracks []U3DMotionTrack, bindings map[string]int, time float64, cache *u3dPlaybackCache) (float64, error) {
	reference := animation.Motions[0]
	if animation.Attributes&1 == 0 {
		time = 0
	}
	time = time*float64(animation.TimeScale)*float64(reference.TimeScale) - float64(reference.TimeOffset)/1000
	if !finite(time) {
		return 0, fmt.Errorf("invalid U3D animation time")
	}
	minimum, maximum := math.Inf(1), math.Inf(-1)
	for i, track := range tracks {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if bindings != nil {
			if index, exists := bindings[track.Name]; !exists || index != i {
				continue
			}
		}
		bounds, err := cache.limits(&tracks[i])
		if err != nil {
			return 0, err
		}
		minimum, maximum = min(minimum, bounds[0]), max(maximum, bounds[1])
	}
	if math.IsInf(minimum, 1) {
		return 0, fmt.Errorf("empty U3D animation tracks")
	}
	if reference.Attributes&1 != 0 && maximum > minimum {
		time = math.Mod(time-minimum, maximum-minimum)
		if time < 0 {
			time += maximum - minimum
		}
		time += minimum
	}
	return max(minimum, min(time, maximum)), r.ctx.Err()
}

// sampleAnimation 求值单个动作的节点或骨骼姿态，锁定根骨骼时保留其初始变换
// 入参: state 本节点累计状态, node 节点, mesh 模型几何, animation 修饰器, motion 动作, chain 链类别, time 时间, cache 本次采样缓存
// 返回: error 轨道、修饰顺序、预算或取消错误
func (r *u3dRender) sampleAnimation(state *u3dAnimationState, node *U3DNode, mesh *U3DMesh, animation *U3DAnimation, motion *U3DMotion, chain int, time float64, cache *u3dPlaybackCache) error {
	if animation.Attributes&4 != 0 {
		if chain == 0 {
			return fmt.Errorf("unsupported U3D resource single-track animation")
		}
		time, err := r.animationTime(animation, motion.Tracks[:1], nil, time, cache)
		if err != nil {
			return err
		}
		frame, err := cache.sample(&motion.Tracks[0], time)
		if err != nil {
			return err
		}
		matrix, err := frame.Matrix()
		if err != nil {
			return err
		}
		if animation.Attributes&2 == 0 {
			state.matrix = state.matrix.multiply(u3dMatrix(matrix))
			if !state.matrix.valid() {
				return fmt.Errorf("invalid U3D animated node transform")
			}
		}
		return nil
	}
	if node.Kind != "Model" || mesh == nil || len(mesh.Bones) == 0 {
		return fmt.Errorf("missing U3D animation skeleton")
	}
	if state.weights != nil {
		return fmt.Errorf("unsupported U3D successive skeletal animations")
	}
	if err := r.reserve(uint64(len(mesh.Bones)), 96); err != nil {
		return err
	}
	defer func() { r.remaining += uint64(len(mesh.Bones)) * 96 }()
	tracks := make(map[string]int, len(mesh.Bones))
	for i, bone := range mesh.Bones {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if _, exists := tracks[bone.Name]; exists {
			return fmt.Errorf("duplicate U3D bone %q", bone.Name)
		}
		tracks[bone.Name] = -1
	}
	bound := 0
	for i, track := range motion.Tracks {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if index, exists := tracks[track.Name]; exists {
			if index >= 0 {
				return fmt.Errorf("ambiguous U3D animation track %q", track.Name)
			}
			tracks[track.Name] = i
			bound++
		}
	}
	if bound == 0 {
		return nil
	}
	time, err := r.animationTime(animation, motion.Tracks, tracks, time, cache)
	if err != nil {
		return err
	}
	if err := r.reserve(uint64(len(mesh.Bones)), 64); err != nil {
		return err
	}
	state.bones = make([]U3DBoneState, 0, len(mesh.Bones))
	for i, bone := range mesh.Bones {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return err
			}
		}
		if i == 0 && animation.Attributes&2 != 0 {
			continue
		}
		if index := tracks[bone.Name]; index >= 0 {
			frame, err := cache.sample(&motion.Tracks[index], time)
			if err != nil {
				return err
			}
			state.bones = append(state.bones, U3DBoneState{Name: bone.Name, Frame: frame})
		}
	}
	if len(state.bones) == 0 {
		r.remaining += uint64(cap(state.bones)) * 64
		state.bones = nil
		return nil
	}
	resourceEnd, nodeEnd := uint64(animation.Index), uint64(0)
	if chain != 0 {
		resourceEnd, nodeEnd = math.MaxUint64, uint64(animation.Index)
	}
	state.weights, err = r.skinWeightSet(mesh, node.BoneWeights, resourceEnd, nodeEnd)
	return err
}
