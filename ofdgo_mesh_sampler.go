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
	"image/color"
	"math"
	"slices"
)

// MeshSampler 保存独立网格快照和空间索引，支持并发采样
type MeshSampler struct {
	mesh  MeshShading
	nodes []meshSampleNode
}

// meshSampleNode 保存连续三角形组的包围盒及右子树位置
type meshSampleNode struct {
	minX, minY, maxX, maxY float64
	right                  int
}

// NewSampler 创建网格快照，后续修改原网格不影响采样结果
// 返回: *MeshSampler 只读采样器
func (m *MeshShading) NewSampler() *MeshSampler {
	s := &MeshSampler{}
	if m != nil {
		s.mesh = *m
		s.mesh.Triangles = slices.Clone(m.Triangles)
		if len(m.Triangles) > 8 {
			s.build(0, len(m.Triangles))
		}
	}
	return s
}

// At 采样快照中的预乘颜色，后绘制的三角形优先
// 入参: x 横坐标, y 纵坐标
// 返回: color.RGBA 预乘颜色
func (s *MeshSampler) At(x, y float64) color.RGBA {
	if len(s.nodes) == 0 || !finite(x) || !finite(y) {
		return s.mesh.At(x, y)
	}
	if value, ok := s.sample(0, 0, len(s.mesh.Triangles), x, y); ok {
		return value
	}
	return s.mesh.Background
}

// build 按绘制顺序构建包围盒层级，不重排原三角形
// 入参: first 起始三角形, end 结束位置
// 返回: int 节点位置
func (s *MeshSampler) build(first, end int) int {
	index := len(s.nodes)
	s.nodes = append(s.nodes, meshSampleNode{})
	if end-first <= 8 {
		node := meshSampleNode{minX: math.Inf(1), minY: math.Inf(1), maxX: math.Inf(-1), maxY: math.Inf(-1)}
		for _, triangle := range s.mesh.Triangles[first:end] {
			a, b, c := triangle[0].Point, triangle[1].Point, triangle[2].Point
			d := (b.Y-c.Y)*(a.X-c.X) + (c.X-b.X)*(a.Y-c.Y)
			if !finite(d) {
				node = meshSampleNode{minX: math.Inf(-1), minY: math.Inf(-1), maxX: math.Inf(1), maxY: math.Inf(1)}
				break
			}
			for _, vertex := range triangle {
				node.minX, node.minY = math.Min(node.minX, vertex.Point.X), math.Min(node.minY, vertex.Point.Y)
				node.maxX, node.maxY = math.Max(node.maxX, vertex.Point.X), math.Max(node.maxY, vertex.Point.Y)
			}
		}
		if finite(node.minX) && finite(node.minY) && finite(node.maxX) && finite(node.maxY) {
			dx := 2e-12*(node.maxX-node.minX) + 1e-14*math.Max(math.Abs(node.minX), math.Abs(node.maxX))
			dy := 2e-12*(node.maxY-node.minY) + 1e-14*math.Max(math.Abs(node.minY), math.Abs(node.maxY))
			node.minX, node.maxX = math.Nextafter(node.minX-dx, math.Inf(-1)), math.Nextafter(node.maxX+dx, math.Inf(1))
			node.minY, node.maxY = math.Nextafter(node.minY-dy, math.Inf(-1)), math.Nextafter(node.maxY+dy, math.Inf(1))
		}
		s.nodes[index] = node
		return index
	}
	middle := first + (end-first)/2
	left := s.build(first, middle)
	right := s.build(middle, end)
	a, b := s.nodes[left], s.nodes[right]
	s.nodes[index] = meshSampleNode{minX: math.Min(a.minX, b.minX), minY: math.Min(a.minY, b.minY), maxX: math.Max(a.maxX, b.maxX), maxY: math.Max(a.maxY, b.maxY), right: right}
	return index
}

// sample 优先查询后绘制的区域，包围盒外跳过整组三角形
// 入参: index 节点位置, first 起始三角形, end 结束位置, x 横坐标, y 纵坐标
// 返回: color.RGBA 采样颜色, bool 是否命中
func (s *MeshSampler) sample(index, first, end int, x, y float64) (color.RGBA, bool) {
	node := s.nodes[index]
	if x < node.minX || x > node.maxX || y < node.minY || y > node.maxY {
		return color.RGBA{}, false
	}
	if end-first <= 8 {
		for i := end - 1; i >= first; i-- {
			if value, ok := meshTriangleColor(&s.mesh.Triangles[i], x, y); ok {
				return value, true
			}
		}
		return color.RGBA{}, false
	}
	middle := first + (end-first)/2
	if value, ok := s.sample(node.right, middle, end, x, y); ok {
		return value, true
	}
	return s.sample(index+1, first, middle, x, y)
}
