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
	"errors"
	"fmt"
	"math"
	"path"
	"reflect"
	"strings"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// errPDFMediaRead 标识媒体读取失败，避免作为可恢复的导航问题忽略
var errPDFMediaRead = errors.New("PDF media read failed")

// pdfMediaFileKey 区分媒体文件及注册用途，避免重复解码或调用文件解析器
type pdfMediaFileKey struct {
	Stream        *pdfgo.Stream
	Name          string
	System        pdfgo.Name
	Specification uintptr
	Attachment    bool
	Kind          string
	Format        string
}

// pdfMediaResource 保留资源标识和外部文件说明，避免缓存期间字典地址被复用
type pdfMediaResource struct {
	ID         string
	Dictionary pdfgo.Dictionary
}

// pdfMediaInstanceKey 区分播放目标与资源文件，避免不同注解共用控制状态
type pdfMediaInstanceKey struct {
	Page       int
	Reference  pdfgo.Reference
	Dictionary uintptr
	Resource   string
}

// mediaInstance 为播放目标分配独立标识，同一目标的动作继续共用控制状态
// 入参: id 已注册的媒体资源, target 目标注解, page 所属页面索引
// 返回: string 独立资源标识, error 资源复制或取消错误
func (p *pdfImporter) mediaInstance(id string, target pdfgo.Annotation, page int) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}
	key := pdfMediaInstanceKey{Page: page, Reference: target.Reference, Resource: id}
	if key.Reference == (pdfgo.Reference{}) {
		key.Dictionary = reflect.ValueOf(target.Dictionary).Pointer()
	}
	if resource, exists := p.mediaInstances[key]; exists {
		return resource.ID, nil
	}
	copy, err := p.editor.CopyMedia(id)
	if err != nil {
		return "", err
	}
	if p.mediaInstances == nil {
		p.mediaInstances = make(map[pdfMediaInstanceKey]pdfMediaResource)
	}
	p.mediaInstances[key] = pdfMediaResource{ID: copy, Dictionary: target.Dictionary}
	return copy, nil
}

// mediaResource 复用媒体文件的注册结果，区分格式、类别及附件用途
// 入参: ctx 取消上下文, file 文件说明, kind 媒体类别, format 文件格式, attachment 是否作为附件保存
// 返回: string 资源标识，外部资源不可用时为空, error 读取、注册或取消错误
func (p *pdfImporter) mediaResource(ctx context.Context, file pdfgo.FileSpecification, kind, format string, attachment bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := pdfMediaFileKey{Stream: file.Embedded, Name: file.Name, System: file.FileSystem, Attachment: attachment, Kind: kind, Format: format}
	if file.Embedded == nil && file.Dictionary != nil {
		key.Specification = reflect.ValueOf(file.Dictionary).Pointer()
	}
	if resource, exists := p.mediaResources[key]; exists {
		return resource.ID, nil
	}
	data, available, err := p.mediaData(ctx, file)
	if err != nil {
		return "", err
	}
	var id string
	if available {
		if attachment {
			id, err = p.attachmentFile(file.Name, data)
		} else {
			id, err = p.editor.AddMedia(kind, format, data)
		}
		if err != nil {
			return "", err
		}
	}
	if p.mediaResources == nil {
		p.mediaResources = make(map[pdfMediaFileKey]pdfMediaResource)
	}
	p.mediaResources[key] = pdfMediaResource{ID: id, Dictionary: file.Dictionary}
	return id, ctx.Err()
}

// soundResource 注册采样音频或调用方提供的外部音频，不自动访问文件
// 入参: ctx 取消上下文, sound 音频对象
// 返回: string 资源标识，不可用时为空, error 读取或封装错误
func (p *pdfImporter) soundResource(ctx context.Context, sound pdfgo.Sound) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var data []byte
	var err error
	format := "WAV"
	if sound.File != nil {
		var available bool
		data, available, err = p.mediaData(ctx, *sound.File)
		if err != nil || !available {
			return "", err
		}
		format = codecpdf.MediaFormat(*sound.File)
	} else {
		data, err = codecpdf.SoundWAV(ctx, sound)
	}
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return p.editor.AddMedia("Audio", format, data)
}

// soundLinkAction 保留音频点击动作，混音、交互限制及音量损失按策略报告
// 入参: object 音频动作, strict 是否禁止播放参数损失
// 返回: *Action 音频动作, error 结构、参数或资源错误
func (p *pdfImporter) soundLinkAction(object pdfgo.Object, strict bool) (*Action, error) {
	source, err := p.reader.ReadSoundAction(p.ctx, object)
	if err != nil {
		return nil, err
	}
	if source.Volume < 0 {
		return nil, &pdfgo.UnsupportedError{Feature: "negative sound action volume conversion"}
	}
	if strict {
		return nil, &pdfgo.UnsupportedError{Feature: "sound action playback policy conversion"}
	}
	volume := int(math.Round(source.Volume * 100))
	if source.Mix {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action mixing policy not transferred to OFD"})
	} else {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action exclusive playback policy not transferred to OFD"})
	}
	if source.Synchronous {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action interaction restriction not transferred to OFD; action waiting retained"})
	}
	if float64(volume)/100 != source.Volume {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF sound action volume rounded to OFD integer volume"})
	}
	id, err := p.soundResource(p.ctx, source.Sound)
	if err != nil || id == "" {
		return nil, err
	}
	return &Action{Event: "CLICK", Sound: &Sound{ResourceID: id, Volume: &volume, Repeat: source.Repeat, Synchronous: source.Synchronous}}, nil
}

// interactiveAnnotation 保留3D或富媒体的静态外观和原始资源，不执行脚本
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, strict 严格检查开关
// 返回: error 外观、资源或不可等价转换错误
func (p *pdfImporter) interactiveAnnotation(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strict {
		return &pdfgo.UnsupportedError{Feature: "interactive " + string(annotation.Subtype) + " conversion"}
	}
	var actions []Action
	if annotation.Subtype == "3D" {
		return p.threeDAnnotation(ctx, page, annotation)
	} else {
		media, err := p.reader.ReadRichMedia(ctx, annotation)
		if err != nil {
			return p.interactiveAppearanceFallback(ctx, page, annotation, "metadata", err)
		}
		config := media.Configurations[media.ActiveConfiguration]
		var playing *pdfgo.FileSpecification
		if (config.Subtype == "Sound" || config.Subtype == "Video") && len(config.Instances) == 1 && config.Instances[0].Subtype == config.Subtype {
			condition, err := p.reader.Resolve(media.Activation["Condition"])
			if err != nil {
				return err
			}
			style, err := p.reader.Resolve(media.Presentation["Style"])
			if err != nil {
				return err
			}
			if (condition == nil || condition == pdfgo.Name("XA")) && (style == nil || style == pdfgo.Name("Embedded")) && media.Activation["Scripts"] == nil {
				file := config.Instances[0].Asset
				data, available, err := p.mediaData(ctx, file)
				if err != nil {
					return err
				}
				if available {
					kind := "Video"
					if config.Subtype == "Sound" {
						kind = "Audio"
					}
					id, err := p.editor.AddMedia(kind, codecpdf.MediaFormat(file), data)
					if err != nil {
						return err
					}
					if kind == "Audio" {
						actions = append(actions, Action{Event: "CLICK", Sound: &Sound{ResourceID: id}})
					} else {
						actions = append(actions, Action{Event: "CLICK", Movie: &Movie{ResourceID: id, Operator: "Play"}})
					}
				}
				playing = &file
			}
		}
		files := append([]pdfgo.MediaAsset(nil), media.Assets...)
		seen := make(map[*pdfgo.Stream]bool)
		external := make(map[string]bool)
		if playing != nil {
			if playing.Embedded != nil {
				seen[playing.Embedded] = true
			} else {
				external[string(playing.FileSystem)+"\x00"+playing.Name] = true
			}
		}
		for _, config := range media.Configurations {
			for _, instance := range config.Instances {
				files = append(files, pdfgo.MediaAsset{File: instance.Asset})
			}
		}
		for index, asset := range files {
			key := string(asset.File.FileSystem) + "\x00" + asset.File.Name
			if asset.File.Embedded != nil && seen[asset.File.Embedded] || asset.File.Embedded == nil && external[key] {
				continue
			}
			data, available, err := p.mediaData(ctx, asset.File)
			if err != nil {
				return err
			}
			if available {
				name := path.Base(strings.ReplaceAll(asset.File.Name, "\\", "/"))
				if name == "" || name == "." || name == "/" {
					name = fmt.Sprintf("Media_%d_%d", p.page+1, index+1)
				}
				_, err = p.editor.AddAttachment(name, data)
				if err != nil {
					return err
				}
			}
			if asset.File.Embedded != nil {
				seen[asset.File.Embedded] = true
			} else {
				external[key] = true
			}
		}
	}
	if err := p.appearanceAnnotation(ctx, page, annotation, actions...); err != nil {
		return err
	}
	p.report.Warnings = append(p.report.Warnings, pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF " + string(annotation.Subtype) + " appearance and available primary assets retained; advanced interaction not transferred to OFD"})
	return nil
}

// interactiveAppearanceFallback 保留交互注解的有效正常外观，取消时不继续转换
// 入参: ctx 取消上下文, page PDF页面, annotation 交互注解, kind 失败资源类型, cause 原始错误
// 返回: error 取消或外观转换错误
func (p *pdfImporter) interactiveAppearanceFallback(ctx context.Context, page *pdfgo.Page, annotation pdfgo.Annotation, kind string, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	if err := p.appearanceAnnotation(ctx, page, annotation); err != nil {
		return err
	}
	p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: fmt.Sprintf("PDF %s %s unavailable; normal appearance retained: %v", annotation.Subtype, kind, cause)})
	return nil
}

// mediaData 读取媒体或附件，宽松模式缺少外部读取器时报告资源不可用
// 入参: ctx 取消上下文, file PDF文件说明
// 返回: []byte 原始文件, bool 是否可用, error 解码、外部读取或严格模式错误
func (p *pdfImporter) mediaData(ctx context.Context, file pdfgo.FileSpecification) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if file.Embedded == nil && p.resolveFile == nil && p.warning != nil {
		p.warning(pdfgo.Diagnostic{Page: p.page + 1, Message: "PDF external file unavailable: " + file.Name + "; appearance retained"})
		return nil, false, nil
	}
	data, err := p.reader.ReadFileData(ctx, file, p.resolveFile)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %w", errPDFMediaRead, err)
	}
	return data, true, nil
}
