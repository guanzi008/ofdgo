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
	"math"
)

// U3DMotion 保存骨骼或节点动作轨道，解码不执行动画
type U3DMotion struct {
	Name                                                 string
	TimeInverseQuantization, RotationInverseQuantization float32
	Tracks                                               []U3DMotionTrack
}

// U3DMotionTrack 保存命名轨道及其关键帧，保持文件中的时间与帧次序
type U3DMotionTrack struct {
	Name                                                      string
	DisplacementInverseQuantization, ScaleInverseQuantization float32
	Frames                                                    []U3DKeyFrame
}

// U3DKeyFrame 保存关键帧时间及相对父项的变换，Rotation按W、X、Y、Z排列
type U3DKeyFrame struct {
	Time                float32
	Displacement, Scale [3]float32
	Rotation            [4]float32
}

// motion 读取ECMA-363第9.8.6节的动作资源，首尾帧保留未量化数值
// 入参: values 字段读取器, name 资源名
// 返回: error 结构、数值、预算或取消错误
func (d *u3dDecoder) motion(values *u3dValues, name string) error {
	r := u3dResourceReader{values: values, decoder: d}
	defer r.release()
	count := r.u32()
	motion := U3DMotion{Name: name, TimeInverseQuantization: r.f32(), RotationInverseQuantization: r.f32()}
	if err := r.err(); err != nil {
		return err
	}
	if motion.TimeInverseQuantization < 0 || motion.RotationInverseQuantization < 0 {
		return fmt.Errorf("invalid U3D motion quantization")
	}
	if err := d.reserve(uint64(count), 48); err != nil {
		return err
	}
	motion.Tracks = make([]U3DMotionTrack, int(count))
	for index := range motion.Tracks {
		if err := d.ctx.Err(); err != nil {
			return err
		}
		track := &motion.Tracks[index]
		track.Name = r.text()
		frames := r.u32()
		track.DisplacementInverseQuantization, track.ScaleInverseQuantization = r.f32(), r.f32()
		if err := r.err(); err != nil {
			return err
		}
		if track.DisplacementInverseQuantization < 0 || track.ScaleInverseQuantization < 0 {
			return fmt.Errorf("invalid U3D motion track quantization")
		}
		if err := d.reserve(uint64(frames), 44); err != nil {
			return err
		}
		track.Frames = make([]U3DKeyFrame, int(frames))
		for i := range track.Frames {
			if i%256 == 0 {
				if err := d.ctx.Err(); err != nil {
					return err
				}
			}
			frame := &track.Frames[i]
			if i == 0 || i == len(track.Frames)-1 {
				frame.Time = r.f32()
				for component := range frame.Displacement {
					frame.Displacement[component] = r.f32()
				}
				for component := range frame.Rotation {
					frame.Rotation[component] = r.f32()
				}
				for component := range frame.Scale {
					frame.Scale[component] = r.f32()
				}
			} else if err := r.motionFrame(frame, track.Frames[i-1], &motion, track); err != nil {
				return err
			}
			if err := r.err(); err != nil {
				return err
			}
		}
	}
	if err := r.done(); err != nil {
		return err
	}
	d.model.Motions = append(d.model.Motions, motion)
	return nil
}

// motionFrame 按前一帧预测时间、位移和缩放，并右乘旋转差分四元数
// 入参: frame 目标帧, previous 前一帧, motion 动作量化参数, track 轨道量化参数
// 返回: error 符号、数值或读取错误
func (r *u3dResourceReader) motionFrame(frame *U3DKeyFrame, previous U3DKeyFrame, motion *U3DMotion, track *U3DMotionTrack) error {
	time, err := r.motionDifference([]float32{previous.Time}, motion.TimeInverseQuantization, 0)
	if err != nil {
		return err
	}
	frame.Time = time[0]
	position, err := r.motionDifference(previous.Displacement[:], track.DisplacementInverseQuantization, 2)
	if err != nil {
		return err
	}
	copy(frame.Displacement[:], position[:])
	signs := r.value(4, true)
	if signs > 15 {
		return fmt.Errorf("invalid U3D motion rotation signs")
	}
	var difference [4]float64
	var squared float64
	for component := 1; component < 4; component++ {
		value := float64(r.value(5, false)) * float64(motion.RotationInverseQuantization)
		if signs&(1<<component) != 0 {
			value = -value
		}
		difference[component] = value
		squared += value * value
	}
	if err := r.err(); err != nil {
		return err
	}
	if squared > 1 {
		return fmt.Errorf("invalid U3D motion rotation difference")
	}
	difference[0] = math.Sqrt(1 - squared)
	if signs&1 != 0 {
		difference[0] = -difference[0]
	}
	p := previous.Rotation
	w, x, y, z := float64(p[0]), float64(p[1]), float64(p[2]), float64(p[3])
	a, b, c, e := difference[0], difference[1], difference[2], difference[3]
	frame.Rotation = [4]float32{
		float32(w*a - x*b - y*c - z*e),
		float32(w*b + x*a + y*e - z*c),
		float32(w*c - x*e + y*a + z*b),
		float32(w*e + x*c - y*b + z*a),
	}
	for _, value := range frame.Rotation {
		if !finite(float64(value)) {
			return fmt.Errorf("invalid U3D reconstructed rotation")
		}
	}
	scale, err := r.motionDifference(previous.Scale[:], track.ScaleInverseQuantization, 6)
	copy(frame.Scale[:], scale[:])
	return err
}

// motionDifference 用共享幅值上下文恢复一至三个差分分量
// 入参: previous 前一帧分量, scale 逆量化系数, context 符号上下文，下一项为幅值上下文
// 返回: [3]float32 重建分量, error 符号、数值或读取错误
func (r *u3dResourceReader) motionDifference(previous []float32, scale float32, context int) (values [3]float32, err error) {
	signs := r.value(context, true)
	if signs >= 1<<len(previous) {
		return values, fmt.Errorf("invalid U3D motion difference signs")
	}
	for index, prior := range previous {
		difference := float64(r.value(context+1, false)) * float64(scale)
		if signs&(1<<index) != 0 {
			difference = -difference
		}
		values[index] = float32(float64(prior) + difference)
		if !finite(float64(values[index])) {
			return values, fmt.Errorf("invalid U3D reconstructed motion value")
		}
	}
	return values, r.err()
}
