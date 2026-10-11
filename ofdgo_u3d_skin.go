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
	"fmt"
	"math"
	"slices"
)

// U3DBoneState 按骨骼名称指定相对父骨骼末端的姿态，根骨骼相对模型原点
// Frame可由动作轨道采样得到，Time不参与变形；单位缩放需显式设为[3]float32{1, 1, 1}
// 未覆盖项沿用文件或动画求值后的局部姿态，重复名称使用最后一项，不施加逆运动学约束
type U3DBoneState struct {
	Name  string
	Frame U3DKeyFrame
}

// u3dSkinBone 保存初始姿态到当前姿态的变换及其逆矩阵
type u3dSkinBone struct {
	matrix, inverse u3dMatrix
	invertible      bool
}

// u3dSkinPose 保存同一几何最近求值的姿态和权重，仅用于本次绘制
type u3dSkinPose struct {
	weights *U3DBoneWeights
	states  []U3DBoneState
	mesh    *U3DMesh
}

// nodeMesh 返回节点本次绘制使用的几何，未变形节点直接引用原资源
// 入参: node 模型节点
// 返回: *U3DMesh 几何，资源缺失时为空
func (r *u3dRender) nodeMesh(node *U3DNode) *U3DMesh {
	if mesh := r.posed[node.Name]; mesh != nil {
		return mesh
	}
	if index, exists := r.meshes[node.Resource]; exists {
		return &r.model.Meshes[index]
	}
	return nil
}

// poseNodes 应用动画和显式骨骼姿态，同源同姿态的节点及父路径共用变形几何
// 返回: error 骨架、权重、预算或取消错误
func (r *u3dRender) poseNodes() error {
	if len(r.states) == 0 && len(r.animated) == 0 {
		return r.ctx.Err()
	}
	var cache map[*U3DMesh]u3dSkinPose
	var cacheBytes uint64
	defer func() { r.remaining += cacheBytes }()
	for i := range r.model.Nodes {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.connected != nil && !r.connected[i] {
			continue
		}
		node := &r.model.Nodes[i]
		states := r.states[node.Name].Bones
		animation := r.animated[node.Name]
		if len(states) == 0 && len(animation.bones) == 0 {
			continue
		}
		mesh := r.nodeMesh(node)
		if node.Kind != "Model" || mesh == nil {
			return fmt.Errorf("missing U3D bone state geometry for %q", node.Name)
		}
		if r.posed == nil {
			if err := r.reserve(1, 1024); err != nil {
				return err
			}
			r.posed = make(map[string]*U3DMesh)
		}
		if err := r.reserve(1, 128); err != nil {
			return err
		}
		var temporary uint64
		if len(animation.bones) != 0 {
			if len(states) == 0 {
				states = animation.bones
			} else {
				count := uint64(len(animation.bones)) + uint64(len(states))
				if err := r.reserve(count, 64); err != nil {
					return err
				}
				temporary = count * 64
				combined := make([]U3DBoneState, len(animation.bones), int(count))
				copy(combined, animation.bones)
				states = append(combined, states...)
			}
		}
		weights := animation.weights
		if weights == nil {
			var err error
			weights, err = r.skinWeightSource(mesh, node.BoneWeights, math.MaxUint64, math.MaxUint64)
			if err != nil {
				r.remaining += temporary
				return err
			}
		}
		previous, exists := cache[mesh]
		shared := exists && previous.weights == weights && len(previous.states) == len(states)
		if shared {
			for j := range states {
				if j%256 == 0 {
					if err := r.ctx.Err(); err != nil {
						r.remaining += temporary
						return err
					}
				}
				if states[j] != previous.states[j] {
					shared = false
					break
				}
			}
		}
		if shared {
			r.posed[node.Name] = previous.mesh
			r.remaining += temporary
			continue
		}
		source, sets := mesh, node.BoneWeights
		if animation.weights != nil {
			geometry := *mesh
			geometry.BoneWeights = []U3DBoneWeights{*animation.weights}
			mesh, sets = &geometry, nil
		}
		posed, err := r.skinMesh(mesh, sets, states)
		if err != nil {
			r.remaining += temporary
			return err
		}
		r.posed[node.Name] = posed
		if len(r.model.Nodes) > 1 {
			if cache == nil {
				if err := r.reserve(1, 1024); err != nil {
					return err
				}
				cacheBytes += 1024
				cache = make(map[*U3DMesh]u3dSkinPose)
			}
			if !exists {
				if err := r.reserve(1, 128); err != nil {
					return err
				}
				cacheBytes += 128
			}
			if err := r.reserve(uint64(len(states)), 64); err != nil {
				return err
			}
			cacheBytes += uint64(len(states)) * 64
			saved := make([]U3DBoneState, len(states))
			copy(saved, states)
			cache[source] = u3dSkinPose{weights: weights, states: saved, mesh: posed}
			released := uint64(len(previous.states)) * 64
			r.remaining += released
			cacheBytes -= released
		}
		r.remaining += temporary
	}
	return r.ctx.Err()
}

// boneTransforms 连接骨骼局部姿态，以初始姿态的逆变换将作者坐标映射到当前姿态
// 入参: bones 按父项在前排列的骨架, states 显式姿态
// 返回: []u3dSkinBone 变形矩阵, error 骨架、姿态或取消错误
func (r *u3dRender) boneTransforms(bones []U3DBone, states []U3DBoneState) ([]u3dSkinBone, error) {
	index := make(map[string]int, len(bones))
	for i, bone := range bones {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		if _, exists := index[bone.Name]; exists {
			return nil, fmt.Errorf("duplicate U3D bone %q", bone.Name)
		}
		index[bone.Name] = i
	}
	overrides := make(map[int]U3DKeyFrame, min(len(states), len(bones)))
	for i, state := range states {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		bone, exists := index[state.Name]
		if !exists {
			return nil, fmt.Errorf("missing U3D bone %q", state.Name)
		}
		overrides[bone] = state.Frame
	}
	bind, pose := make([]u3dMatrix, len(bones)), make([]u3dMatrix, len(bones))
	result := make([]u3dSkinBone, len(bones))
	for i, bone := range bones {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		if !finite(float64(bone.Length)) || bone.Length < 0 || bone.Attributes & ^uint32(255) != 0 {
			return nil, fmt.Errorf("invalid U3D bone parameters")
		}
		if bone.Attributes&1 != 0 && bone.LinkCount != 0 {
			return nil, fmt.Errorf("unsupported U3D bone link deformation")
		}
		initial := U3DKeyFrame{Displacement: bone.Displacement, Rotation: bone.Orientation, Scale: [3]float32{1, 1, 1}}
		base, err := initial.Matrix()
		if err != nil {
			return nil, err
		}
		current := base
		if frame, exists := overrides[i]; exists {
			current, err = frame.Matrix()
			if err != nil {
				return nil, err
			}
		}
		bind[i], pose[i] = u3dMatrix(base), u3dMatrix(current)
		if i == 0 {
			if bone.Parent != "" {
				return nil, fmt.Errorf("invalid U3D root bone parent")
			}
		} else {
			parent, exists := index[bone.Parent]
			if !exists || parent >= i {
				return nil, fmt.Errorf("invalid U3D bone parent %q", bone.Parent)
			}
			end := u3dIdentity()
			end[12] = float64(bones[parent].Length)
			bind[i] = bind[parent].multiply(end).multiply(bind[i])
			pose[i] = pose[parent].multiply(end).multiply(pose[i])
		}
		inverse, ok := bind[i].inverse()
		if !ok || !pose[i].valid() {
			return nil, fmt.Errorf("invalid U3D bone transform")
		}
		result[i].matrix = pose[i].multiply(inverse)
		if !result[i].matrix.valid() {
			return nil, fmt.Errorf("invalid U3D bone deformation")
		}
		result[i].inverse, result[i].invertible = result[i].matrix.inverse()
	}
	return result, nil
}

// skinWeights 选取与几何类型相符的末项权重，节点链覆盖资源链
// 入参: mesh 原始几何, sets 节点权重
// 返回: *U3DBoneWeights 有效权重, error 类型、引用或取消错误
func (r *u3dRender) skinWeights(mesh *U3DMesh, sets []U3DBoneWeights) (*U3DBoneWeights, error) {
	return r.skinWeightSet(mesh, sets, math.MaxUint64, math.MaxUint64)
}

// skinWeightSource 按修饰链截止位置选取权重，不使用动画后面的权重输出
// 入参: mesh 原始几何, sets 节点权重, resourceEnd、nodeEnd 两条链中首个不参与的索引
// 返回: *U3DBoneWeights 权重来源，内容尚未校验, error 类型、引用或取消错误
func (r *u3dRender) skinWeightSource(mesh *U3DMesh, sets []U3DBoneWeights, resourceEnd, nodeEnd uint64) (*U3DBoneWeights, error) {
	kind := uint32(0)
	for i, count := range []int{len(mesh.Faces), len(mesh.Lines), len(mesh.Points)} {
		if count != 0 {
			if kind != 0 {
				return nil, fmt.Errorf("unsupported U3D mixed geometry deformation")
			}
			kind = 1 << i
		}
	}
	var selected *U3DBoneWeights
	for chain, group := range [][]U3DBoneWeights{mesh.BoneWeights, sets} {
		end := resourceEnd
		if chain != 0 {
			end = nodeEnd
		}
		var last *U3DBoneWeights
		for i := range group {
			if i%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return nil, err
				}
			}
			if uint64(group[i].Index) < end && group[i].Attributes == kind && (last == nil || group[i].Index > last.Index) {
				last = &group[i]
			}
		}
		if last != nil {
			selected = last
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("unsupported U3D deformation without bone weights")
	}
	return selected, r.ctx.Err()
}

// skinWeightSet 选取并校验指定修饰链位置之前的权重
// 入参: mesh 原始几何, sets 节点权重, resourceEnd、nodeEnd 两条链中首个不参与的索引
// 返回: *U3DBoneWeights 有效权重, error 类型、引用、数值或取消错误
func (r *u3dRender) skinWeightSet(mesh *U3DMesh, sets []U3DBoneWeights, resourceEnd, nodeEnd uint64) (*U3DBoneWeights, error) {
	selected, err := r.skinWeightSource(mesh, sets, resourceEnd, nodeEnd)
	if err != nil {
		return nil, err
	}
	if selected.Index == 0 || len(selected.Offsets) == 0 || len(selected.Offsets)-1 > len(mesh.Positions) || selected.Offsets[0] != 0 || uint64(selected.Offsets[len(selected.Offsets)-1]) != uint64(len(selected.Weights)) {
		return nil, fmt.Errorf("invalid U3D bone weight offsets")
	}
	for i := 1; i < len(selected.Offsets); i++ {
		if err := r.ctx.Err(); err != nil {
			return nil, err
		}
		start, end := selected.Offsets[i-1], selected.Offsets[i]
		if end < start || uint64(end) > uint64(len(selected.Weights)) {
			return nil, fmt.Errorf("invalid U3D bone weight range")
		}
		var total float64
		for j, weight := range selected.Weights[start:end] {
			if j%256 == 0 {
				if err := r.ctx.Err(); err != nil {
					return nil, err
				}
			}
			if uint64(weight.Bone) >= uint64(len(mesh.Bones)) || !finite(float64(weight.Weight)) || weight.Weight < 0 {
				return nil, fmt.Errorf("invalid U3D bone weight")
			}
			total += float64(weight.Weight)
		}
		if start != end && math.Abs(total-1) > 1e-6 {
			return nil, fmt.Errorf("invalid U3D bone weight sum")
		}
	}
	return selected, nil
}

// influence 返回指定位置的骨骼权重，未提供权重的位置保持不变
// 入参: position 作者位置索引
// 返回: []U3DBoneWeight 影响列表
func (weights *U3DBoneWeights) influence(position uint32) []U3DBoneWeight {
	if uint64(position)+1 >= uint64(len(weights.Offsets)) {
		return nil
	}
	return weights.Weights[weights.Offsets[position]:weights.Offsets[position+1]]
}

// skinVector 对位置或法线应用加权骨骼变换，法线在累加后统一归一化
// 入参: value 原向量, weights 位置权重, bones 骨骼变换, normal 是否处理法线
// 返回: [3]float32 变形结果, error 数值、奇异变换或取消错误
func (r *u3dRender) skinVector(value [3]float32, weights []U3DBoneWeight, bones []u3dSkinBone, normal bool) ([3]float32, error) {
	input := u3dVector{float64(value[0]), float64(value[1]), float64(value[2])}
	for _, component := range input {
		if !finite(component) {
			return [3]float32{}, fmt.Errorf("invalid U3D skin vector")
		}
	}
	if len(weights) == 0 {
		return value, nil
	}
	var result u3dVector
	for i, weight := range weights {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return [3]float32{}, err
			}
		}
		if weight.Weight == 0 {
			continue
		}
		bone := &bones[weight.Bone]
		v := bone.matrix.point(input)
		if normal {
			if !bone.invertible {
				return [3]float32{}, fmt.Errorf("unsupported U3D singular bone normal transform")
			}
			m := bone.inverse
			v = u3dVector{m[0]*input[0] + m[1]*input[1] + m[2]*input[2], m[4]*input[0] + m[5]*input[1] + m[6]*input[2], m[8]*input[0] + m[9]*input[1] + m[10]*input[2]}
		}
		for axis := range result {
			result[axis] += v[axis] * float64(weight.Weight)
		}
	}
	for _, component := range result {
		if !finite(component) {
			return [3]float32{}, fmt.Errorf("invalid U3D skin result")
		}
	}
	if normal {
		result = result.unit()
	}
	for axis := range value {
		value[axis] = float32(result[axis])
		if !finite(float64(value[axis])) {
			return [3]float32{}, fmt.Errorf("U3D skin result exceeds range")
		}
	}
	return value, nil
}

// skinMesh 复制受变形影响的属性，按位置与法线的组合保留独立法线索引
// 入参: mesh 原始几何, sets 节点权重, states 显式骨骼姿态
// 返回: *U3DMesh 独立姿态几何, error 结构、能力、预算或取消错误
func (r *u3dRender) skinMesh(mesh *U3DMesh, sets []U3DBoneWeights, states []U3DBoneState) (*U3DMesh, error) {
	weights, err := r.skinWeights(mesh, sets)
	if err != nil {
		return nil, err
	}
	if err := r.reserve(uint64(len(mesh.Bones)), 768); err != nil {
		return nil, err
	}
	if err := r.reserve(uint64(len(states)), 128); err != nil {
		r.remaining += uint64(len(mesh.Bones)) * 768
		return nil, err
	}
	temporary := uint64(len(mesh.Bones))*768 + uint64(len(states))*128
	defer func() { r.remaining += temporary }()
	bones, err := r.boneTransforms(mesh.Bones, states)
	if err != nil {
		return nil, err
	}
	if err := r.reserve(1, 768); err != nil {
		return nil, err
	}
	if err := r.reserve(uint64(len(mesh.Positions)), 12); err != nil {
		return nil, err
	}
	result := *mesh
	result.Positions = make([][3]float32, len(mesh.Positions))
	for i, position := range mesh.Positions {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		result.Positions[i], err = r.skinVector(position, weights.influence(uint32(i)), bones, false)
		if err != nil {
			return nil, err
		}
	}
	if mesh.ExcludeNormals {
		return &result, r.ctx.Err()
	}
	bytes := uint64(len(mesh.Faces))*148 + uint64(len(mesh.Lines))*100 + uint64(len(mesh.Points))*52
	if err := r.reserve(bytes, 1); err != nil {
		return nil, err
	}
	result.Faces, result.Lines, result.Points = slices.Clone(mesh.Faces), slices.Clone(mesh.Lines), slices.Clone(mesh.Points)
	result.Normals = nil
	if err := r.reserve(1, 1024); err != nil {
		return nil, err
	}
	index := make(map[[2]uint32]uint32)
	defer func() { r.remaining += 1024 + uint64(len(index))*96 }()
	for i := range len(result.Faces) + len(result.Lines) + len(result.Points) {
		if i%256 == 0 {
			if err := r.ctx.Err(); err != nil {
				return nil, err
			}
		}
		_, corners := result.primitive(i)
		for j := range corners {
			corner := &corners[j]
			if uint64(corner.Position) >= uint64(len(mesh.Positions)) || uint64(corner.Normal) >= uint64(len(mesh.Normals)) {
				return nil, fmt.Errorf("invalid U3D skin corner index")
			}
			key := [2]uint32{corner.Position, corner.Normal}
			normal, exists := index[key]
			if !exists {
				if err := r.reserve(1, 96); err != nil {
					return nil, err
				}
				if len(result.Normals) == cap(result.Normals) {
					capacity := max(16, uint64(cap(result.Normals))*2)
					if capacity > math.MaxUint32 {
						return nil, fmt.Errorf("U3D skin normal count exceeds limit")
					}
					if err := r.reserve(capacity, 12); err != nil {
						return nil, err
					}
					grown := make([][3]float32, len(result.Normals), int(capacity))
					copy(grown, result.Normals)
					r.remaining += uint64(cap(result.Normals)) * 12
					result.Normals = grown
				}
				value, err := r.skinVector(mesh.Normals[corner.Normal], weights.influence(corner.Position), bones, true)
				if err != nil {
					return nil, err
				}
				normal = uint32(len(result.Normals))
				index[key] = normal
				result.Normals = append(result.Normals, value)
			}
			corner.Normal = normal
		}
	}
	return &result, r.ctx.Err()
}
