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

// structureObjectVisitor 收集页面及复合图元标识，绑定校验不解码字体、图片或裁剪
type structureObjectVisitor struct {
	objects map[string]bool
	targets []StructureTarget
}

// StructureObjects 列出指定页可绑定的图元及原文，包含模板、隐藏内容和复合图元成员
// 不解码字体和图片，返回顺序遵循页面遍历顺序，同一标识仅列出一次
// 入参: page 从0开始的页面索引
// 返回: []StructureTarget 候选图元，不计算Bounds, error 页面或遍历错误
func (r *Renderer) StructureObjects(page int) ([]StructureTarget, error) {
	content, err := r.Reader.PageContentByIndex(page)
	if err != nil {
		return nil, err
	}
	visitor := &structureObjectVisitor{objects: make(map[string]bool)}
	if err := r.WalkPage(content, visitor); err != nil {
		return nil, err
	}
	for i := range visitor.targets {
		visitor.targets[i].Page, visitor.targets[i].PageID = page+1, content.ID
	}
	return visitor.targets, nil
}

// BeginObject 登记图元标识，不创建分组状态
// 入参: object 页面图元
// 返回: bool 始终为false
func (v *structureObjectVisitor) BeginObject(object *GraphicObject) bool {
	id := editorResourceID(editorObjectID(*object))
	if id != "" && !v.objects[id] {
		v.objects[id] = true
		target := StructureTarget{ID: id}
		if object.Type == "TextObject" {
			target.Text = object.TextObject.Text()
		}
		v.targets = append(v.targets, target)
	}
	return false
}

// EndObject 满足分组访问接口，不维护分组栈
func (v *structureObjectVisitor) EndObject() {}

// BeginAnnotation 不为注解容器分配正文图元标识
// 入参: id 注解标识
// 返回: bool 始终为false
func (v *structureObjectVisitor) BeginAnnotation(id string) bool { return false }

// SkipObjects 不依赖图元序号
// 入参: count 跳过数量
func (v *structureObjectVisitor) SkipObjects(count int) {}

// IncludeHidden 允许绑定隐藏图元
// 返回: bool 始终为true
func (v *structureObjectVisitor) IncludeHidden() bool { return true }

// SkipClips 绑定校验不需要裁剪轮廓
// 返回: bool 始终为true
func (v *structureObjectVisitor) SkipClips() bool { return true }

// DrawObject 不绘制图元，标识已由BeginObject登记
// 入参: object 页面图元, state 继承状态
// 返回: error 始终为nil
func (v *structureObjectVisitor) DrawObject(object *GraphicObject, state RenderState) error {
	return nil
}

// DrawStamp 不将签章外观作为正文绑定目标
// 入参: stamp 签章外观
// 返回: error 始终为nil
func (v *structureObjectVisitor) DrawStamp(stamp Stamp) error { return nil }
