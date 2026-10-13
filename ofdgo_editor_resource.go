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
	"crypto/sha256"
	"encoding/xml"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

// Resources 获取当前编辑快照的资源，覆盖正文、模板、注解、图案和复合图元
// 入参: ctx 取消上下文
// 返回: []ResourceInfo 资源及引用位置, error 快照或结构错误
func (e *Editor) Resources(ctx context.Context) ([]ResourceInfo, error) {
	reader, err := e.ReaderContext(ctx)
	if err != nil {
		return nil, err
	}
	return reader.Resources(ctx)
}

// ImportMedia 导入可独立管理的音视频资源，即使尚未绑定动作也保留在当前文档中
// 不解码或转码，格式由调用方提供；修改隔离其他文档及历史版本，支持撤销
// 入参: ctx 取消上下文, kind 为Audio或Video, format 媒体格式, data 完整媒体数据
// 返回: string 新资源标识, error 数据或包结构错误
func (e *Editor) ImportMedia(ctx context.Context, kind, format string, data []byte) (string, error) {
	if kind != "Audio" && kind != "Video" || len(data) == 0 {
		return "", fmt.Errorf("nonempty Audio or Video resource required")
	}
	reader, err := e.ReaderContext(ctx)
	if err != nil {
		return "", err
	}
	working, err := reader.Editor()
	if err != nil {
		return "", err
	}
	id, err := working.AddMedia(kind, format, data)
	if err != nil {
		return "", err
	}
	resource := working.resources[len(working.resources)-1]
	encoded, err := working.encodeXML(func(x *ofdXML) { x.resources(nil, []editorResource{resource}, nil) })
	if err != nil {
		return "", err
	}
	doc, err := reader.DocContext(ctx)
	if err != nil {
		return "", err
	}
	parts := map[string][]byte{resource.name: resource.data}
	name, added, err := mergePackageResources(ctx, reader, parts, doc, working.packageDirectory(), encoded)
	if err != nil {
		return "", err
	}
	entry := reader.documentRoot
	content, err := reader.readFileViewContext(ctx, entry)
	if err != nil {
		return "", err
	}
	root, err := parseEditorXMLContext(ctx, content)
	if err != nil {
		return "", err
	}
	common := root.child("CommonData")
	if common == nil || common.child("MaxUnitID") == nil {
		return "", fmt.Errorf("missing CommonData or MaxUnitID")
	}
	maximum := common.child("MaxUnitID")
	patches := []editorXMLPatch{{maximum.start, maximum.end, maximum.textXML("MaxUnitID", strconv.Itoa(working.maxID))}}
	if added {
		position := common.close
		for _, child := range common.children {
			if child.matchesOFD("TemplatePage") || child.matchesOFD("DefaultCS") {
				position = child.start
				break
			}
		}
		patches = append(patches, editorXMLPatch{position, position, common.textXML("DocumentRes", "/"+name)})
	}
	parts[entry] = editorPatchXML(content, patches)
	scan := &editorFontDocument{reader: reader, index: reader.DocumentIndex()}
	if err := scan.apply(ctx, parts); err != nil {
		return "", err
	}
	return id, e.commitResourceSnapshot(ctx, reader)
}

// ReplaceResource 替换当前文档图片、音视频或附件的文件，保留标识和全部使用位置
// 图片同时清除受影响对象的旧替换图及复合预览；字体须使用EmbedFont或ExternalizeFont
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识, format 媒体格式，图片按实际数据确定, data 完整文件
// 返回: error 校验或提交错误，失败时文档不变
func (e *Editor) ReplaceResource(ctx context.Context, kind, id, format string, data []byte) error {
	if kind == "Font" {
		return fmt.Errorf("use font binding operations for fonts")
	}
	return e.changeResource(ctx, kind, id, func(reader *Reader, item ResourceInfo, target editorFontText, changes map[string][]byte) error {
		if len(data) == 0 {
			return fmt.Errorf("empty media data")
		}
		if kind == "Image" {
			_, actual, err := decodeImageDataContext(ctx, data)
			if err != nil {
				return err
			}
			format = actual
		}
		if item.Size == uint64(len(data)) && strings.EqualFold(item.Format, strings.TrimSpace(format)) {
			digest, err := outputResourceDigest(ctx, reader, nil, item.File, make([]byte, 32<<10))
			if err != nil {
				return err
			}
			if digest == sha256.Sum256(data) {
				return nil
			}
		}
		name := packageAvailableName(reader, changes, path.Join(e.packageDirectory(), "Res", "Media", "Media_"+item.ID))
		changes[name] = bytes.Clone(data)
		fragment, err := editorXMLStandalone(target.file.data[target.node.start:target.node.end], target.node)
		if err != nil {
			return err
		}
		root, err := parseEditorXML(fragment)
		if err != nil {
			return err
		}
		file := root.child("MediaFile")
		if file == nil {
			return fmt.Errorf("missing MediaFile")
		}
		attribute, err := editorXMLAttributePatch(fragment, root, "Format", strings.ToUpper(strings.TrimSpace(format)))
		if err != nil {
			return err
		}
		fragment = editorPatchXML(fragment, []editorXMLPatch{attribute, {file.start, file.end, file.textXML("MediaFile", "/"+name)}})
		changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{target.node.start, target.node.end, fragment}})
		return nil
	}, data)
}

// DeleteResource 删除未被引用的资源声明，仅清理全包中可确认无引用的文件
// 未知扩展导致引用不完整时拒绝删除；其他文档及历史版本保持不变
// 入参: ctx 取消上下文, kind 资源类型, id 资源标识
// 返回: error 仍有引用、结构或提交错误
func (e *Editor) DeleteResource(ctx context.Context, kind, id string) error {
	return e.changeResource(ctx, kind, id, nil, nil)
}

// changeResource 校验引用后在独立快照中修改声明，借用既有版本隔离和历史提交路径
// 入参: ctx 取消上下文, kind 资源类型, id 标识, replace 替换操作，nil表示删除, data 附件替换数据
// 返回: error 引用、结构或提交错误
func (e *Editor) changeResource(ctx context.Context, kind, id string, replace func(*Reader, ResourceInfo, editorFontText, map[string][]byte) error, data []byte) error {
	reader, err := e.ReaderContext(ctx)
	if err != nil {
		return err
	}
	catalog, err := reader.resourceCatalog(ctx)
	if err != nil {
		return err
	}
	item, err := catalog.find(kind, id)
	if err != nil {
		return err
	}
	if !item.Complete {
		return fmt.Errorf("resource references contain unsupported content")
	}
	if replace == nil && len(item.References) != 0 {
		return fmt.Errorf("resource is still referenced")
	}
	if kind == "Attachment" {
		working, err := reader.Editor()
		if err != nil {
			return err
		}
		if replace == nil {
			err = working.DeleteAttachment(item.ID)
		} else {
			err = working.ReplaceAttachment(item.ID, data)
		}
		if err != nil {
			return err
		}
		reader, err = working.ReaderContext(ctx)
		if err != nil {
			return err
		}
		return e.commitResourceSnapshot(ctx, reader)
	}
	target := catalog.nodes[item.Kind+"/"+item.ID]
	changes := make(map[string][]byte)
	if replace == nil {
		node := target.node
		if node.parent != nil && len(node.parent.children) == 1 {
			node = node.parent
		}
		changes[target.file.name] = editorPatchXML(target.file.data, []editorXMLPatch{{start: node.start, end: node.end}})
	} else if err := replace(reader, item, target, changes); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	if replace != nil && kind == "Image" {
		if err := invalidateResourcePreviews(ctx, catalog.scan, item.ID, changes); err != nil {
			return err
		}
	}
	if err := catalog.scan.apply(ctx, changes); err != nil {
		return err
	}
	if err := pruneUnreferencedFiles(ctx, reader, item.File); err != nil {
		return err
	}
	return e.commitResourceSnapshot(ctx, reader)
}

// invalidateResourcePreviews 移除图片内容变化后失效的替换图及逐级复合预览
// 入参: ctx 取消上下文, scan 当前文档结构, id 图片标识, changes 待提交文件
// 返回: error 解析或取消错误
func invalidateResourcePreviews(ctx context.Context, scan *editorFontDocument, id string, changes map[string][]byte) error {
	affected := map[string]bool{id: true}
	for {
		count := len(affected)
		for name, file := range scan.files {
			data, exists := changes[name]
			if !exists {
				data = file.data
			}
			root, err := parseEditorXMLContext(ctx, data)
			if err != nil {
				return err
			}
			var patches []editorXMLPatch
			var visit func(*editorXML) (bool, error)
			visit = func(node *editorXML) (bool, error) {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				if !node.matchesOFD(node.name.Local) {
					return false, nil
				}
				primary := affected[editorResourceID(node.attr("ResourceID"))] || affected[editorResourceID(node.attr("ImageMask"))]
				changed := primary || affected[editorResourceID(node.attr("Substitution"))]
				if node.matchesOFD("ImageObject") && primary && node.hasAttr("Substitution") {
					fragment, err := editorXMLStandalone(data[node.start:node.end], node)
					if err != nil {
						return false, err
					}
					var object ImageObject
					if err := xml.Unmarshal(fragment, &object); err != nil {
						return false, err
					}
					before := GraphicObject{Type: "ImageObject", ImageObject: object}
					after := before
					after.ImageObject.Substitution = ""
					updated, err := editorXMLObject(data, node, before, after)
					if err != nil {
						return false, err
					}
					patches = append(patches, editorXMLPatch{node.start, node.end, updated})
					return true, nil
				}
				for _, child := range node.children {
					used, err := visit(child)
					if err != nil {
						return false, err
					}
					changed = changed || used
				}
				if changed && node.matchesOFD("CompositeGraphicUnit") && node.parent != nil && node.parent.matchesOFD("CompositeGraphicUnits") {
					affected[editorResourceID(node.attr("ID"))] = true
					for _, key := range []string{"Thumbnail", "Substitution"} {
						if child := node.child(key); child != nil {
							patches = append(patches, editorXMLPatch{start: child.start, end: child.end})
						}
					}
				}
				return changed, nil
			}
			if _, err := visit(root); err != nil {
				return err
			}
			if len(patches) != 0 {
				changes[name] = editorPatchXML(data, patches)
			}
		}
		if len(affected) == count {
			return nil
		}
	}
}

// BindMedia 追加或移除指定图元的音视频点击动作，保留其余动作，重复绑定不新增动作
// 入参: ctx 取消上下文, id 媒体标识, page 零基页码, object 图元标识, bind 为false时移除该媒体动作
// 返回: error 资源、对象或提交错误
func (e *Editor) BindMedia(ctx context.Context, id string, page int, object string, bind bool) error {
	kind := ""
	for _, resource := range e.resources {
		if media := resource.image; media != nil && sameResourceID(media.ID, id) && (media.Type == "Audio" || media.Type == "Video") {
			kind, id = media.Type, media.ID
			break
		}
	}
	if kind == "" {
		items, err := e.Resources(ctx)
		if err != nil {
			return err
		}
		for _, item := range items {
			if sameResourceID(item.ID, id) && (item.Kind == "Audio" || item.Kind == "Video") {
				kind, id = item.Kind, item.ID
				break
			}
		}
	}
	if kind == "" {
		return fmt.Errorf("audio or video resource not found")
	}
	target, err := e.Object(page, object)
	if err != nil {
		return err
	}
	var actions []Action
	switch target.Type {
	case "TextObject":
		actions = target.TextObject.Actions
	case "ImageObject":
		actions = target.ImageObject.Actions
	case "Path", "PathObject":
		actions = target.PathObject.Actions
	case "CompositeObject", "CompositeGraphicUnit":
		actions = target.CompositeGraphicUnit.Actions
	default:
		return fmt.Errorf("object does not support actions")
	}
	found := false
	actions = slices.DeleteFunc(slices.Clone(actions), func(action Action) bool {
		match := action.Sound != nil && sameResourceID(action.Sound.ResourceID, id) || action.Movie != nil && sameResourceID(action.Movie.ResourceID, id)
		found = found || match
		return match && !bind
	})
	if bind && !found {
		action := Action{Event: "CLICK"}
		if kind == "Audio" {
			action.Sound = &Sound{ResourceID: id}
		} else {
			action.Movie = &Movie{ResourceID: id, Operator: "Play"}
		}
		actions = append(actions, action)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return e.SetObjectActions(page, []string{object}, actions)
}
