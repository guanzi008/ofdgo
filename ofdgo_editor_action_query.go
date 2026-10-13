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
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
)

// PageActions 获取当前文档及版本中页面自身的完整动作独立副本，不合并模板或执行动作
// 入参: page 从0开始的页面索引
// 返回: []Action 原顺序动作，未设置时为nil, error 页面或解析错误
func (e *Editor) PageActions(page int) ([]Action, error) {
	content, err := e.page(page)
	if err != nil {
		return nil, err
	}
	return cloneEditorData(content.Actions), nil
}

// ObjectActions 获取普通页面图元自身的完整动作独立副本，不展开共享复合资源或执行动作
// 入参: page 从0开始的页面索引, id 与SetObjectActions相同的对象标识
// 返回: []Action 原顺序动作，未设置时为nil, error 页面、对象或类型错误
func (e *Editor) ObjectActions(page int, id string) ([]Action, error) {
	layer, index, err := e.findObject(page, id)
	if err != nil {
		return nil, err
	}
	actions, err := resourceObjectActions(layer.Objects[index])
	if err != nil {
		return nil, err
	}
	return cloneEditorData(actions), nil
}

// OutlineActions 获取当前目录节点自身的完整动作独立副本，不含子目录或执行动作
// 入参: path 从0开始的非空层级索引
// 返回: []Action 原顺序动作，未设置时为nil, error 路径或解析错误
func (e *Editor) OutlineActions(path []int) ([]Action, error) {
	if len(path) == 0 {
		return nil, fmt.Errorf("outline path must not be empty")
	}
	data, err := e.outlineXML()
	if err != nil {
		return nil, err
	}
	root, err := parseEditorXML(data)
	if err != nil {
		return nil, err
	}
	node, err := editorOutlineNode(root, path)
	if err != nil {
		return nil, err
	}
	node = node.child("Actions")
	if node == nil {
		return nil, nil
	}
	data, err = editorXMLStandalone(data[node.start:node.end], node)
	if err != nil {
		return nil, err
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	var actions []Action
	if err := decodeOFDActions(decoder, start.(xml.StartElement), &actions); err != nil {
		return nil, err
	}
	return actions, nil
}

// ValidateActionTargets 校验动作结构及当前文档、版本内的页面、书签、附件和音视频引用
// 可用于提交前检查，不改变现有Set方法允许悬空目标的语义；不检查事件归属或访问外部URI
// 已注册但未绑定的媒体也可引用；仅检查资源声明及类型，不解码媒体或执行动作
// 入参: ctx 取消上下文, actions 待校验动作列表
// 返回: error 首个错误及从0开始的动作索引，取消错误支持errors.Is
func (e *Editor) ValidateActionTargets(ctx context.Context, actions []Action) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var resources []ResourceInfo
	resourcesLoaded := false
	for index, action := range actions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateActions([]Action{action}); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
		if action.Goto != nil {
			dest := action.Goto.Dest
			if bookmark := action.Goto.Bookmark; bookmark != nil {
				if e.source != nil {
					for _, entry := range e.source.document.Bookmarks.Bookmark {
						if err := ctx.Err(); err != nil {
							return err
						}
						if entry.Name == bookmark.Name {
							value := entry.Dest
							dest = &value
						}
					}
				}
				if dest == nil {
					return fmt.Errorf("action %d: bookmark %q not found", index, bookmark.Name)
				}
			}
			if _, err := dest.attributes(); err != nil {
				return fmt.Errorf("action %d: %w", index, err)
			}
			found := false
			for _, page := range e.pages {
				if err := ctx.Err(); err != nil {
					return err
				}
				if sameResourceID(page.ID, dest.PageID) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("action %d: destination page %q not found", index, dest.PageID)
			}
		}
		var kind, id string
		switch {
		case action.GotoA != nil:
			kind, id = "Attachment", action.GotoA.AttachID
		case action.Sound != nil:
			kind, id = "Audio", action.Sound.ResourceID
		case action.Movie != nil:
			kind, id = "Video", action.Movie.ResourceID
		default:
			continue
		}
		registered := false
		if kind != "Attachment" {
			for _, resource := range e.resources {
				if err := ctx.Err(); err != nil {
					return err
				}
				if media := resource.image; media != nil && media.Type == kind && sameResourceID(media.ID, id) {
					registered = true
					break
				}
			}
		}
		if registered {
			continue
		}
		if !resourcesLoaded {
			var err error
			resources, err = e.Resources(ctx)
			if err != nil {
				return fmt.Errorf("action %d: %w", index, err)
			}
			resourcesLoaded = true
		}
		if _, err := findResource(resources, kind, id); err != nil {
			return fmt.Errorf("action %d: %w", index, err)
		}
	}
	return ctx.Err()
}
