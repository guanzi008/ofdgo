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
	"reflect"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfClipPath 保存导入器只读的裁剪几何，不缓存对象位移或文字裁剪
type pdfClipPath struct {
	box  Box
	data string
	path *[1]PathObject
}

// clipPath 复用页面内重复路径编码，按估算字节预算淘汰且不缓存错误
// 入参: path 裁剪路径
// 返回: pdfClipPath 变换后的边界及编码, error 取消错误
func (p *pdfImporter) clipPath(path pdfgo.Path) (pdfClipPath, error) {
	key, err := codecpdf.ClipPathKey(p.ctx, path, p.matrix)
	if err != nil {
		return pdfClipPath{}, err
	}
	if p.clipPaths == nil {
		p.clipPaths = &renderCache[[32]byte, pdfClipPath]{limit: 8 << 20}
	}
	if cached, ok := p.clipPaths.get(key); ok {
		return cached, nil
	}
	box, err := p.pathBoundsContext(p.ctx, path)
	if err != nil {
		return pdfClipPath{}, err
	}
	data, err := p.pathDataContext(p.ctx, path, box)
	if err != nil {
		return pdfClipPath{}, err
	}
	rule := "NonZero"
	if path.EvenOdd {
		rule = "Even-Odd"
	}
	boundary := pdfBoundary(box)
	fill, stroke := true, false
	result := pdfClipPath{box: box, data: data, path: &[1]PathObject{{Boundary: boundary, AbbreviatedData: data, Rule: rule, Fill: &fill, Stroke: &stroke}}}
	p.clipPaths.put(key, result, len(data)+len(boundary)+512)
	return result, nil
}

// sharedClipObjects 将连续重复的大裁剪区提取为复合对象，同步缓冲区及来源区段
// 入参: objects 本库生成且尚未分配标识的对象
// 返回: []GraphicObject 提取后的对象, error 资源创建或取消错误
func (p *pdfImporter) sharedClipObjects(objects []GraphicObject) ([]GraphicObject, error) {
	var output []GraphicObject
	var mapped pdfObjectBuffer
	for start := 0; start < len(objects); {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		clips := pdfPageClip(objects[start])
		end := start + 1
		if clips != nil {
			for end < len(objects) && p.objects.origin(start).equal(p.objects.origin(end)) && pdfMatchesPageClip(objects[end], clips) {
				if err := p.ctx.Err(); err != nil {
					return nil, err
				}
				end++
			}
		}
		if end-start < 2 {
			if output != nil {
				mapped.recordOrigin(len(output), p.objects.origin(start))
				output = append(output, objects[start])
			}
			start = end
			continue
		}
		if output == nil {
			output = append(make([]GraphicObject, 0, len(objects)), objects[:start]...)
			if p.objects != nil {
				for _, run := range p.objects.sequences {
					if run.start >= start {
						break
					}
					mapped.sequences = append(mapped.sequences, run)
				}
			}
		}
		members := objects[start:end:end]
		for index := range members {
			*editorObjectClips(&members[index]) = nil
		}
		id, err := p.objectValidation().addOwnedVector(p.ctx, members, p.pageWidth, p.pageHeight)
		if err != nil {
			return nil, err
		}
		mapped.recordOrigin(len(output), p.objects.origin(start))
		output = append(output, GraphicObject{Type: "CompositeObject", CompositeGraphicUnit: CompositeGraphicUnit{
			Boundary: pdfBoundary(Box{W: p.pageWidth, H: p.pageHeight}), ResourceID: id, Clips: clips,
		}})
		start = end
	}
	if output == nil {
		return objects, nil
	}
	if p.objects != nil {
		mapped.parts = [][]GraphicObject{output}
		mapped.size = len(output)
		*p.objects = mapped
	}
	return output, nil
}

// pdfPageClip 将导入裁剪区还原到页面坐标，仅提取包含大量重复路径数据的裁剪
// 入参: object 导入对象
// 返回: *Clips 页面坐标裁剪，不满足提取条件时为空
func pdfPageClip(object GraphicObject) *Clips {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return nil
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.usesObjectMatrix() || len(clips.Clip) == 0 {
		return nil
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return nil
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	bytes := 0
	result := &Clips{Clip: make([]Clip, len(clips.Clip))}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return nil
		}
		area := clip.Area[0]
		area.CTM = ""
		result.Clip[index].Area = []ClipArea{area}
		for _, path := range area.Path {
			bytes += len(path.AbbreviatedData)
		}
	}
	if bytes < 2048 {
		return nil
	}
	return result
}

// pdfMatchesPageClip 比较页面坐标裁剪，不将相同外接矩形视为相同路径
// 入参: object 导入对象, pageClip 已还原的裁剪区
// 返回: bool 是否可共享裁剪区
func pdfMatchesPageClip(object GraphicObject, pageClip *Clips) bool {
	if object.Type != "TextObject" && object.Type != "PathObject" && object.Type != "ImageObject" {
		return false
	}
	clips := *editorObjectClips(&object)
	if clips == nil || clips.usesObjectMatrix() || len(clips.Clip) != len(pageClip.Clip) {
		return false
	}
	boundary, _ := editorGeometry(object)
	box, err := ParseBox(boundary)
	if err != nil {
		return false
	}
	translation := ""
	if box.X != 0 || box.Y != 0 {
		translation = pdfNumbers(1, 0, 0, 1, -box.X, -box.Y)
	}
	for index, clip := range clips.Clip {
		if len(clip.Area) != 1 || clip.Area[0].CTM != translation {
			return false
		}
		area, expected := clip.Area[0], pageClip.Clip[index].Area[0]
		if area.DrawParam != expected.DrawParam || !reflect.DeepEqual(area.Path, expected.Path) || !reflect.DeepEqual(area.Text, expected.Text) {
			return false
		}
	}
	return true
}

// clips 省略覆盖对象边界的矩形裁剪，复用其余几何并保留位移及路径交集
// 入参: paths 裁剪路径, origin 对象边界
// 返回: *Clips OFD裁剪区域，无裁剪时为空, error 未解析的裁剪字形
func (p *pdfImporter) clips(paths []pdfgo.Path, origin Box) (*Clips, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var clips *Clips
	var areas []ClipArea
	translation := ""
	for _, path := range paths {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}
		if codecpdf.ClipContainsBoundary(path, p.matrix, origin) {
			continue
		}
		if clips == nil {
			clips = &Clips{Clip: make([]Clip, 0, len(paths))}
			areas = make([]ClipArea, len(paths))
			if origin.X != 0 || origin.Y != 0 {
				translation = pdfNumbers(1, 0, 0, 1, -origin.X, -origin.Y)
			}
		}
		index := len(clips.Clip)
		area := &areas[index]
		area.CTM = translation
		if len(path.Segments) != 0 {
			encoded, err := p.clipPath(path)
			if err != nil {
				return nil, err
			}
			area.Path = encoded.path[:1:1]
		}
		for _, mark := range path.Text {
			objects, ok := p.clipTexts[mark]
			if !ok {
				count := p.objects.len()
				textCount := p.report.TextObjects
				if err := p.text(pdfgo.TextMark{Font: mark.Font, Glyphs: mark.Glyphs, Positions: mark.Positions, Matrix: mark.Matrix, Size: mark.Size, HorizontalScale: mark.HorizontalScale, Mode: 7, Clip: mark, Style: pdfgo.Style{Fill: pdfgo.Paint{Alpha: 1}}}); err != nil {
					return nil, err
				}
				p.objects.truncate(count)
				p.report.TextObjects = textCount
				objects = p.clipTexts[mark]
			}
			if len(path.Text) == 1 {
				area.Text = objects[:len(objects):len(objects)]
			} else {
				area.Text = append(area.Text, objects...)
			}
		}
		if len(area.Path)+len(area.Text) == 0 {
			fill, stroke := true, false
			area.Path = []PathObject{{Boundary: "0 0 1 1", AbbreviatedData: "M 0 0 C", Fill: &fill, Stroke: &stroke}}
		}
		clips.Clip = append(clips.Clip, Clip{Area: areas[index : index+1 : index+1]})
	}
	return clips, nil
}
