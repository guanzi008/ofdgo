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
	"fmt"
	"math"
	"slices"
)

// Transform 精确变换控制点和椭圆轴，退化弧沿投影极值分段保留折返
// 入参: matrix 仿射变换
// 返回: Path 独立路径, error 无效路径或数值溢出
func (p Path) Transform(matrix Matrix) (Path, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	for _, value := range matrix.Values() {
		if !finite(value) {
			return nil, fmt.Errorf("invalid geometry transform")
		}
	}
	matrixScale := math.Max(math.Max(math.Abs(matrix.a), math.Abs(matrix.b)), math.Max(math.Abs(matrix.c), math.Abs(matrix.d)))
	determinant, _ := matrixProductSum(matrix.a, matrix.d, -matrix.b, matrix.c)
	transform := func(p Point) Point {
		x, y := matrix.Transform(p.X, p.Y)
		return Point{x, y}
	}
	var result Path
	if len(p) > 0 {
		result = make(Path, 0, len(p))
	}
	var current, start Point
	for _, s := range p {
		originalSegment := s
		original := s.End
		s.End = transform(s.End)
		switch s.Verb {
		case Move:
			start = original
		case Quad:
			s.Control1 = transform(s.Control1)
		case Cubic:
			s.Control1, s.Control2 = transform(s.Control1), transform(s.Control2)
		case ClosePath:
			original, s.End = start, transform(start)
		case Arc:
			a, err := centerArc(current, originalSegment)
			if err != nil {
				return nil, err
			}
			if current == original {
				current = original
				continue
			}
			if s.RadiusX == 0 || s.RadiusY == 0 {
				s = Segment{Verb: Line, End: s.End}
			} else {
				a.center = transform(a.center)
				a.u.X, a.u.Y = Vector(matrix, a.u.X, a.u.Y)
				a.v.X, a.v.Y = Vector(matrix, a.v.X, a.v.Y)
				scale := math.Max(math.Hypot(a.u.X, a.u.Y), math.Hypot(a.v.X, a.v.Y))
				if !finite(scale) {
					return nil, fmt.Errorf("arc transform overflow")
				}
				if scale == 0 {
					if matrixScale != 0 {
						return nil, fmt.Errorf("arc transform precision exhausted")
					}
					s = Segment{Verb: Line, End: s.End}
				} else {
					ux, uy, vx, vy := a.u.X/scale, a.u.Y/scale, a.v.X/scale, a.v.Y/scale
					det := ux*vy - uy*vx
					if determinant != 0 && det == 0 {
						return nil, fmt.Errorf("arc transform precision exhausted")
					}
					if determinant == 0 {
						fractions := []float64{}
						for _, angle := range []float64{math.Atan2(a.v.X, a.u.X), math.Atan2(a.v.Y, a.u.Y)} {
							for _, angle := range []float64{angle, angle + math.Pi} {
								if f := a.fraction(angle); f > 0 && f < 1 {
									fractions = append(fractions, f)
								}
							}
						}
						slices.Sort(fractions)
						for _, f := range slices.Compact(fractions) {
							result = append(result, Segment{Verb: Line, End: a.point(a.theta + f*a.delta)})
						}
						s = Segment{Verb: Line, End: s.End}
					} else {
						xx, yy, xy := ux*ux+vx*vx, uy*uy+vy*vy, ux*uy+vx*vy
						radius := math.Sqrt((xx + yy + math.Hypot(xx-yy, 2*xy)) / 2)
						s.RadiusX, s.RadiusY = scale*radius, scale*math.Abs(det)/radius
						if s.RadiusY == 0 {
							return nil, fmt.Errorf("arc transform precision exhausted")
						}
						s.Rotation = math.Atan2(2*xy, xx-yy) * 90 / math.Pi
						if det < 0 {
							s.Sweep = !s.Sweep
						}
					}
				}
			}
		}
		result = append(result, s)
		current = original
	}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

// Validate 校验公共路径指令、子路径起点和有限坐标
// 返回: error 无效路径
func (p Path) Validate() error {
	started := false
	for _, s := range p {
		points := [3]Point{s.End, s.Control1, s.Control2}
		count := 1
		switch s.Verb {
		case Move:
			started = true
		case Line:
		case Quad:
			count = 2
		case Cubic:
			count = 3
		case Arc:
			if !finite(s.RadiusX) || !finite(s.RadiusY) || !finite(s.Rotation) || s.RadiusX < 0 || s.RadiusY < 0 {
				return fmt.Errorf("invalid arc parameters")
			}
		case ClosePath:
			count = 0
		default:
			return fmt.Errorf("unsupported geometry command %d", s.Verb)
		}
		if !started {
			return fmt.Errorf("geometry path must start with move")
		}
		for _, point := range points[:count] {
			if !finite(point.X) || !finite(point.Y) {
				return fmt.Errorf("non-finite geometry coordinate")
			}
		}
	}
	return nil
}
