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
	"context"
	"strings"
)

// structureVisitor 按引用收集图元及复合图元成员的原文和页面范围
type structureVisitor struct {
	ctx        context.Context
	renderer   *Renderer
	targets    map[string]*StructureTarget
	stack      []*StructureTarget
	text       map[*StructureTarget]*strings.Builder
	boundsOnly bool
}

// BeginObject 记录当前图元的标引目标，使复合图元引用覆盖其成员
// 入参: object 页面图元
// 返回: bool 是否需要配对结束通知
func (v *structureVisitor) BeginObject(object *GraphicObject) bool {
	target := v.targets[editorResourceID(editorObjectID(*object))]
	if target != nil {
		target.Error = ""
	}
	v.stack = append(v.stack, target)
	return true
}

// EndObject 结束当前图元的标引收集
func (v *structureVisitor) EndObject() {
	v.stack = v.stack[:len(v.stack)-1]
}

// BeginAnnotation 不为注解容器建立额外标引层级
// 入参: id 注解标识
// 返回: bool 始终为false
func (v *structureVisitor) BeginAnnotation(id string) bool { return false }

// SkipObjects 不使用渲染对象序号，隐藏成员不占用额外状态
// 入参: count 跳过的图元数
func (v *structureVisitor) SkipObjects(count int) {}

// IncludeHidden 读取原文时保留隐藏语义，仅定位时跳过隐藏复合图元及注解
// 返回: bool 是否需要隐藏内容
func (v *structureVisitor) IncludeHidden() bool { return !v.boundsOnly }

// SkipClips 保留裁剪语义，以可见范围定位图元
// 返回: bool 始终为false
func (v *structureVisitor) SkipClips() bool { return false }

// DrawObject 按继承变换和裁剪度量被引用的图元，按需汇总原文，不解码图片像素
// 入参: object 页面图元, state 继承状态
// 返回: error 度量或取消错误
func (v *structureVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	if err := v.ctx.Err(); err != nil {
		return err
	}
	wanted := false
	for _, target := range v.stack {
		wanted = wanted || target != nil
	}
	if !wanted {
		return nil
	}
	var box Box
	if !state.Hidden {
		result, err := v.renderer.MeasureObject(*object, MeasureOptions{Defaults: state.Defaults, Parent: state.Parent, BoundaryInCTM: state.BoundaryInCTM, Clip: state.Clip})
		if err != nil {
			return err
		}
		box = result.Bounds
	}
	var text string
	if !v.boundsOnly && object.Type == "TextObject" {
		text = object.TextObject.Text()
	}
	for _, target := range v.stack {
		if target == nil {
			continue
		}
		target.Bounds = unionTextBox(target.Bounds, box)
		if text != "" {
			if target.Text == "" {
				target.Text = text
				continue
			}
			if v.text == nil {
				v.text = make(map[*StructureTarget]*strings.Builder)
			}
			builder := v.text[target]
			if builder == nil {
				builder = new(strings.Builder)
				builder.WriteString(target.Text)
				v.text[target] = builder
			}
			builder.WriteByte('\n')
			builder.WriteString(text)
			target.Text = builder.String()
		}
	}
	return nil
}

// DrawStamp 不将签章外观混入正文标引
// 入参: stamp 签章外观
// 返回: error 取消错误
func (v *structureVisitor) DrawStamp(stamp Stamp) error { return v.ctx.Err() }
