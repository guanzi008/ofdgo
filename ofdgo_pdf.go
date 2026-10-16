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
	"fmt"
	"io"

	codecpdf "github.com/xiaoqidun/ofdgo/internal/codec/pdf"
	"github.com/xiaoqidun/pdfgo"
)

// pdfObjectChunkLimit 限制导入暂存片段的容量，不限制页面对象数量
const pdfObjectChunkLimit = 256

// ErrPDFPassword 表示PDF需要正确的用户或所有者密码
var ErrPDFPassword = pdfgo.ErrPassword

// PDFImportOptions 指定PDF转换的密码、运行时后端、进度通知与检查策略
// Password默认使用PDF安全处理器编码，PasswordUTF8启用UTF-8输入及标准密码准备，空值尝试空密码
// Strict禁止恢复缺失资源或丢失内容语义，默认在报告中记录警告
// RendererOptions复用渲染器字体来源和后端配置，Backends优先于其中的后端设置
// RasterDPI指定局部合成与三维绘制精度，0沿用渲染器DPI，默认300dpi，Strict禁用局部合成
// OnProgress按open、pages、convert及write.*阶段报告进度，total为0表示总量未知
// ResolveFile读取PDF引用的外部媒体、附件、页面及三维纹理，默认不访问网络或本地路径
// ResolveReference可提供已解密的引用页面，目标阅读器由调用方维护
// Halftones提供只读设备命名网屏，所属阅读器由调用方维护，连续色调输出不网屏化
// Compression配置转换输出，有损模式按图片实际尺寸降采样，原始及无损模式保持完整采样
type PDFImportOptions struct {
	Password         []byte
	PasswordUTF8     bool
	Backends         *RenderBackends
	RendererOptions  []RendererOption
	Progress         func(int) error
	OnProgress       func(stage string, completed, total int) error
	Strict           bool
	RasterDPI        float64
	ResolveFile      pdfgo.FileResolver
	ResolveReference pdfgo.ReferenceResolver
	Halftones        map[string]*pdfgo.Halftone
	Compression      CompressionOptions
}

// PDFImportReport 汇总转换页数、对象数、链接数和转换警告
type PDFImportReport struct {
	Pages        int
	TextObjects  int
	PathObjects  int
	ImageObjects int
	Links        int
	Warnings     []pdfgo.Diagnostic
}

// pdfImporter 保存单次转换的对象与资源状态
type pdfImporter struct {
	ctx              context.Context
	reader           *pdfgo.Reader
	editor           *Editor
	renderer         *Renderer
	report           PDFImportReport
	matrix           pdfgo.Matrix
	page             int
	pageActionPage   int
	fontIDs          map[*pdfgo.Font]string
	fonts            map[*pdfgo.Font]*pdfImportedFont
	symbolFonts      map[*pdfgo.Font]*pdfImportedFont
	fontWarnings     map[string]bool
	clipTexts        map[*pdfgo.TextClip][]TextObject
	clipPaths        *renderCache[[32]byte, pdfClipPath]
	cmykSpace        string
	rgbSpace         string
	iccSpaces        map[pdfICCKey]string
	objects          *pdfObjectBuffer
	formResources    *pdfFormCache
	validation       *editorValidation
	pages            map[pdfgo.Reference]*pdfgo.Page
	pageOrder        []*pdfgo.Page
	pageIDs          map[pdfgo.Reference]string
	pageIndexes      map[pdfgo.Reference]int
	destinationBoxes map[pdfgo.Reference]Box
	imageIDs         map[pdfImageKey][]pdfImageResource
	imageContents    *renderCache[pdfImageContentKey, pdfImageContent]
	attachmentIDs    map[pdfAttachmentKey]string
	modelAttachments map[*pdfgo.Stream]string
	mediaResources   map[pdfMediaFileKey]pdfMediaResource
	mediaInstances   map[pdfMediaInstanceKey]pdfMediaResource
	movieBounds      map[*Movie]Box
	renditions       *pdfRenditionImport
	maskClips        map[*pdfgo.SoftMask]pdfgo.Path
	pageBox          pdfgo.Rectangle
	pageWidth        float64
	pageHeight       float64
	pendingPath      *pdfgo.PathMark
	warning          func(pdfgo.Diagnostic)
	rasterDPI        float64
	rasterWarned     bool
	compositeCache   *pdfCompositeCache
	compositeNodes   []pdfCompositeNode
	compositeSpace   *pdfgo.ColorSpace
	patternCells     *renderCache[pdfPatternCellKey, *Pattern]
	annotationData   []byte
	resolveFile      pdfgo.FileResolver
	resolveReference pdfgo.ReferenceResolver
	references       *codecpdf.ReferenceCache
	halftoneWarnings map[*pdfgo.Halftone]bool
	halftones        map[string]*pdfgo.Halftone
	transferBackdrop bool
	transferModel    pdfgo.Name
	compression      CompressionOptions
}

// pdfObjectBuffer 分块收集导入对象，提交前只整理一次连续数据
type pdfObjectBuffer struct {
	parts [][]GraphicObject
	size  int
}

// ImportPDF 将PDF内容转换为独立OFD编辑文档，失败时不返回部分结果
// 入参: ctx 取消上下文, source PDF数据, size 字节数, options 转换选项
// 返回: *Editor 编辑文档, PDFImportReport 转换统计, error 错误信息
func ImportPDF(ctx context.Context, source io.ReaderAt, size int64, options PDFImportOptions) (*Editor, PDFImportReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, PDFImportReport{}, err
	}
	if err := options.Compression.Validate(); err != nil {
		return nil, PDFImportReport{}, err
	}
	if !finite(options.RasterDPI) || options.RasterDPI < 0 {
		return nil, PDFImportReport{}, fmt.Errorf("invalid PDF raster DPI")
	}
	renderer := NewRenderer(&Reader{}, options.RendererOptions...)
	if options.Backends != nil {
		WithRenderBackends(*options.Backends)(renderer)
	}
	if options.RasterDPI != 0 {
		renderer.DPI = options.RasterDPI
	}
	if !finite(renderer.DPI) || renderer.DPI <= 0 {
		return nil, PDFImportReport{}, fmt.Errorf("invalid PDF raster DPI")
	}
	progress := editorProgress(options.OnProgress).withContext(ctx)
	if err := progress.report("open", 0, 0); err != nil {
		return nil, PDFImportReport{}, err
	}
	editor := NewEditor()
	editor.SetRenderBackends(renderer.Backends())
	editor.fontDirs, editor.fontFS = renderer.FontSources()
	editor.fontSourcesCache = renderer.fontSourcesCache
	importer := pdfImporter{ctx: ctx, editor: editor, renderer: renderer, rasterDPI: renderer.DPI, fontIDs: map[*pdfgo.Font]string{}, fonts: map[*pdfgo.Font]*pdfImportedFont{}, pages: map[pdfgo.Reference]*pdfgo.Page{}, pageIDs: map[pdfgo.Reference]string{}}
	readOptions := pdfgo.ReaderOptions{Password: options.Password, PasswordUTF8: options.PasswordUTF8, ShapeText: importer.shapePDFText}
	var diagnostics []pdfgo.Diagnostic
	var readWarning func(pdfgo.Diagnostic)
	if !options.Strict {
		readWarning = func(diagnostic pdfgo.Diagnostic) { diagnostics = append(diagnostics, diagnostic) }
		readOptions.Warning = func(diagnostic pdfgo.Diagnostic) { readWarning(diagnostic) }
	}
	reader, err := pdfgo.NewReaderWithOptions(convertReader{context: ctx, source: source}, size, readOptions)
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	defer reader.Close()
	importer.reader = reader
	outputIntents, err := reader.OutputIntents()
	if err != nil {
		return nil, PDFImportReport{}, fmt.Errorf("import PDF output intents: %w", err)
	}
	importer.report.Warnings = diagnostics
	if len(outputIntents) != 0 {
		if options.Strict {
			return nil, PDFImportReport{}, &pdfgo.UnsupportedError{Feature: "PDF output intent conversion"}
		}
		importer.report.Warnings = append(importer.report.Warnings, pdfgo.Diagnostic{Message: "PDF output intents not transferred to OFD"})
	}
	importer.pageIndexes = make(map[pdfgo.Reference]int)
	importer.resolveFile = options.ResolveFile
	importer.resolveReference = options.ResolveReference
	importer.halftones = options.Halftones
	importer.compression = options.Compression
	importer.references = &codecpdf.ReferenceCache{}
	defer importer.references.Close()
	importer.halftoneWarnings = make(map[*pdfgo.Halftone]bool)
	if security := reader.Encryption(); security != nil && !security.Owner && security.Permissions&0xf3c != 0xf3c {
		if options.Strict {
			return nil, PDFImportReport{}, &pdfgo.UnsupportedError{Feature: "PDF access permission conversion"}
		}
		importer.report.Warnings = append(importer.report.Warnings, pdfgo.Diagnostic{Message: "PDF access permissions not transferred to OFD"})
	}
	if !options.Strict {
		importer.warning = func(warning pdfgo.Diagnostic) {
			warning.Page = importer.page + 1
			importer.report.Warnings = append(importer.report.Warnings, warning)
		}
		readWarning = importer.warning
	}
	if err := progress.report("pages", 0, 0); err != nil {
		return nil, PDFImportReport{}, err
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		_, width, height := codecpdf.PageMatrix(page)
		if _, err := editor.AddPage(width, height); err != nil {
			return err
		}
		importer.pages[page.Reference] = page
		importer.pageOrder = append(importer.pageOrder, page)
		importer.pageIDs[page.Reference] = editor.pages[index].ID
		importer.pageIndexes[page.Reference] = index
		return progress.report("pages", index+1, 0)
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	if len(editor.pages) != 0 {
		importer.page = -1
		if err := importer.documentAttachments(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF attachments: %w", err)
		}
		if err := importer.outlines(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF outlines: %w", err)
		}
		if err := importer.documentOpenActions(ctx); err != nil {
			return nil, PDFImportReport{}, fmt.Errorf("import PDF document actions: %w", err)
		}
		importer.page = 0
	}
	if err := progress.report("convert", 0, len(editor.pages)); err != nil {
		return nil, PDFImportReport{}, err
	}
	err = reader.WalkPages(ctx, func(index int, page *pdfgo.Page) error {
		matrix, width, height := codecpdf.PageMatrix(page)
		importer.matrix = matrix
		importer.page = index
		importer.rasterWarned = false
		importer.pageBox = page.CropBox
		importer.pageWidth = width
		importer.pageHeight = height
		importer.maskClips = make(map[*pdfgo.SoftMask]pdfgo.Path)
		importer.compositeCache = nil
		importer.compositeNodes = nil
		importer.compositeSpace = nil
		importer.patternCells = nil
		importer.clipTexts = make(map[*pdfgo.TextClip][]TextObject)
		importer.clipPaths = nil
		importer.annotationData = nil
		if err := importer.pageOpenActions(ctx, page); err != nil {
			return fmt.Errorf("import PDF page %d actions: %w", index+1, err)
		}
		if err := importer.compositePage(nil, func(visitor pdfgo.Visitor) error { return reader.WalkPage(ctx, page, visitor) }); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.flushPath(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if err := importer.annotations(ctx, page, options.Strict); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		if len(importer.annotationData) != 0 {
			if err := editor.appendAnnotations(index, importer.annotationData, true); err != nil {
				return fmt.Errorf("import PDF page %d: %w", index+1, err)
			}
			importer.annotationData = nil
		}
		if err := importer.commitObjects(); err != nil {
			return fmt.Errorf("import PDF page %d: %w", index+1, err)
		}
		importer.report.Pages++
		if err := progress.report("convert", index+1, len(editor.pages)); err != nil {
			return err
		}
		if options.Progress != nil {
			if err := options.Progress(index + 1); err != nil {
				return err
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, PDFImportReport{}, err
	}
	if importer.report.Pages == 0 {
		return nil, PDFImportReport{}, fmt.Errorf("PDF contains no pages")
	}
	return editor, importer.report, nil
}

// ConvertPDF 将PDF转换并写入OFD，转换阶段失败时不写入目标
// 入参: ctx 取消上下文, source PDF数据, size 字节数, output OFD输出, options 转换选项
// 返回: PDFImportReport 转换统计, error 错误信息
func ConvertPDF(ctx context.Context, source io.ReaderAt, size int64, output io.Writer, options PDFImportOptions) (PDFImportReport, error) {
	editor, report, err := ImportPDF(ctx, source, size, options)
	if err != nil {
		return PDFImportReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return PDFImportReport{}, err
	}
	if options.OnProgress != nil {
		if err := options.OnProgress("write", 0, 0); err != nil {
			return PDFImportReport{}, err
		}
	}
	editor.OnWriteProgress = func(stage string, completed, total int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.OnProgress != nil {
			return options.OnProgress("write."+stage, completed, total)
		}
		return nil
	}
	_, err = editor.WriteToWithOptions(ctx, output, WriteOptions{Compression: options.Compression})
	return report, err
}

// referencePage 衔接PDF访问器与当前导入会话的引用文档缓存
// 入参: ctx 取消上下文, source 来源文档, reference 引用目标
// 返回: *pdfgo.Page 目标页面，不可用时为空, error 取消或调用方解析错误
func (p *pdfImporter) referencePage(ctx context.Context, source *pdfgo.Reader, reference pdfgo.ReferenceXObject) (*pdfgo.Page, error) {
	if p.references == nil {
		p.references = &codecpdf.ReferenceCache{}
	}
	return p.references.Resolve(ctx, source, reference, p.resolveFile, p.resolveReference, p.warning)
}

// objectValidation 在单次导入中共享不可变资源的校验结果，隔离局部编辑器
// 返回: *editorValidation 当前编辑器的构建校验会话
func (p *pdfImporter) objectValidation() *editorValidation {
	if p.validation == nil || p.validation.Editor != p.editor {
		p.validation = &editorValidation{Editor: p.editor}
	}
	return p.validation
}

// appendObject 追加本次导入对象，按需建立独立暂存缓冲
// 入参: object 独立导入对象
func (p *pdfImporter) appendObject(object GraphicObject) {
	if p.objects == nil {
		p.objects = &pdfObjectBuffer{}
	}
	p.objects.append(object)
}

// append 追加对象，小批量连续存储，超过阈值后不复制前序片段
// 入参: object 独立对象
func (b *pdfObjectBuffer) append(object GraphicObject) {
	index := len(b.parts) - 1
	if index == 0 && len(b.parts[0]) == cap(b.parts[0]) && cap(b.parts[0]) < pdfObjectChunkLimit {
		part := b.parts[0]
		next := make([]GraphicObject, len(part), min(pdfObjectChunkLimit, max(1, cap(part)*2)))
		copy(next, part)
		b.parts[0] = next
	}
	if index < 0 || len(b.parts[index]) == cap(b.parts[index]) {
		capacity := pdfObjectChunkLimit
		if index < 0 {
			capacity = 1
		}
		b.parts = append(b.parts, make([]GraphicObject, 0, capacity))
		index++
	}
	b.parts[index] = append(b.parts[index], object)
	b.size++
}

// len 获取暂存对象数量，空缓冲返回零
// 返回: int 对象数量
func (b *pdfObjectBuffer) len() int {
	if b == nil {
		return 0
	}
	return b.size
}

// truncate 回滚到已有对象位置，清理丢弃对象及片段引用
// 入参: size 保留对象数量
func (b *pdfObjectBuffer) truncate(size int) {
	if size < 0 || size > b.len() {
		panic("invalid PDF object buffer size")
	}
	if size == b.len() {
		return
	}
	remaining := size
	for index, part := range b.parts {
		if remaining <= len(part) {
			clear(part[remaining:])
			b.parts[index] = part[:remaining]
			clear(b.parts[index+1:])
			b.parts = b.parts[:index+1]
			b.size = size
			return
		}
		remaining -= len(part)
	}
}

// data 按绘制顺序整理独立对象数组，取消时保留原片段供重试
// 入参: ctx 取消上下文
// 返回: []GraphicObject 暂存数据, error 取消错误
func (b *pdfObjectBuffer) data(ctx context.Context) ([]GraphicObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.len() == 0 {
		return nil, nil
	}
	if len(b.parts) == 1 {
		return b.parts[0][:b.size:b.size], nil
	}
	objects := make([]GraphicObject, b.size)
	offset := 0
	for _, part := range b.parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		offset += copy(objects[offset:], part)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.parts[0] = objects
	clear(b.parts[1:])
	b.parts = b.parts[:1]
	return objects, nil
}

// commitObjects 将当前页面待转换对象写入编辑文档
// 返回: error 对象写入错误
func (p *pdfImporter) commitObjects() error {
	if p.objects.len() == 0 {
		return nil
	}
	objects, err := p.objects.data(p.ctx)
	if err != nil {
		return err
	}
	objects, err = p.sharedClipObjects(objects)
	if err != nil {
		return err
	}
	if err := p.objectValidation().appendOwnedObjects(p.ctx, p.page, objects); err != nil {
		return err
	}
	p.objects = nil
	return nil
}
