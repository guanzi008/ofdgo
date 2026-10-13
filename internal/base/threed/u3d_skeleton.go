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
	"io"
)

// U3DBone 保存相对父骨骼的初始位姿，Orientation按W、X、Y、Z排列
// Attributes标志决定链接、关节字段是否存在，以及各轴旋转约束是否启用和限幅
// RotationConstraints按X、Y、Z轴排列，每项依次为上限、下限；未指定骨骼姿态时不变形
type U3DBone struct {
	Name, Parent         string
	Attributes           uint32
	Length               float32
	Displacement         [3]float32
	Orientation          [4]float32
	LinkCount            uint32
	LinkLength           float32
	StartJoint, EndJoint U3DBoneJoint
	RotationConstraints  [3][2]float32
}

// U3DBoneJoint 保存骨骼局部坐标系内关节椭圆的中心偏移和两轴尺寸
type U3DBoneJoint struct {
	Center, Scale [2]float32
}

// skeleton 按ECMA-363第9.6.1.1.6节读取骨架，三角网格、点集和线集共用
// 入参: r 字段读取器
// 返回: []U3DBone 按父项在前排列的骨骼, error 结构、预算或取消错误
func (d *u3dDecoder) skeleton(r *u3dValues) ([]U3DBone, error) {
	count := r.u32()
	if r.err != nil {
		return nil, r.err
	}
	if count == 0 {
		return nil, nil
	}
	if uint64(count) > uint64(len(r.data)-r.pos)/64 {
		return nil, io.ErrUnexpectedEOF
	}
	if err := d.reserve(uint64(count), 256); err != nil {
		return nil, err
	}
	bones := make([]U3DBone, int(count))
	names := make(map[string]bool, int(count))
	for index := range bones {
		if index%256 == 0 {
			if err := d.ctx.Err(); err != nil {
				return nil, err
			}
		}
		bone := &bones[index]
		bone.Name, bone.Parent = r.text(), r.text()
		bone.Attributes, bone.Length = r.u32(), r.f32()
		for component := range bone.Displacement {
			bone.Displacement[component] = r.f32()
		}
		for component := range bone.Orientation {
			bone.Orientation[component] = r.f32()
		}
		if bone.Attributes&1 != 0 {
			bone.LinkCount, bone.LinkLength = r.u32(), r.f32()
		}
		if bone.Attributes&2 != 0 {
			for _, joint := range []*U3DBoneJoint{&bone.StartJoint, &bone.EndJoint} {
				joint.Center = [2]float32{r.f32(), r.f32()}
				joint.Scale = [2]float32{r.f32(), r.f32()}
			}
		}
		for axis := range bone.RotationConstraints {
			bone.RotationConstraints[axis] = [2]float32{r.f32(), r.f32()}
		}
		if r.err != nil {
			return nil, r.err
		}
		if bone.Attributes & ^uint32(255) != 0 {
			return nil, fmt.Errorf("invalid U3D bone attributes")
		}
		if names[bone.Name] {
			return nil, fmt.Errorf("duplicate U3D bone %q", bone.Name)
		}
		if index == 0 && bone.Parent != "" || index != 0 && !names[bone.Parent] {
			return nil, fmt.Errorf("invalid U3D bone parent %q", bone.Parent)
		}
		names[bone.Name] = true
	}
	return bones, nil
}
