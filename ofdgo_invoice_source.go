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
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// InvoiceDigest 计算原始导出数据的SHA256摘要
// 入参: data 导出数据
// 返回: string 小写十六进制摘要
func InvoiceDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadInvoice 读取当前文档及版本声明的发票，标引按对象引用提取文本
// 入参: ctx 取消上下文, source 精确来源
// 返回: InvoiceReport 发票及诊断, error 来源或读取错误
func (r *Reader) ReadInvoice(ctx context.Context, source InvoiceSource) (InvoiceReport, error) {
	ctx = invoiceWriteContext(ctx)
	data, err := r.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceReport{}, err
	}
	report, err := r.readInvoiceXML(ctx, data, source)
	report.DocumentIndex = r.DocumentIndex()
	if r.versionInfo != nil {
		report.Version = r.versionInfo.ID
	}
	return report, err
}

// readInvoiceXML 在当前文档作用域解析业务载荷或对象引用标引
// 入参: ctx 取消上下文, data 原始XML, source 来源
// 返回: InvoiceReport 发票及诊断, error 读取错误
func (r *Reader) readInvoiceXML(ctx context.Context, data []byte, source InvoiceSource) (InvoiceReport, error) {
	report, err := parseInvoiceXML(ctx, data, source)
	if err != nil || source.Kind != "tag" || len(report.Invoices) != 0 {
		return report, err
	}
	root, err := invoiceXMLTree(ctx, data)
	if err != nil {
		return report, err
	}
	if root.name.Space != ofdNamespace2016 || root.name.Local != "root" || !invoiceObjectReferences(root) {
		return report, nil
	}
	structure, err := parseStructure(ctx, data, CustomTag{TypeID: structureTypeOfficial, NameSpace: ofdNamespace2016})
	if err != nil {
		return report, err
	}
	known := false
	for _, node := range structure.Children {
		known = known || node.Namespace == ofdNamespace2016 && node.Name == "InvoiceNo"
	}
	if !known {
		return report, nil
	}
	doc, err := r.DocContext(ctx)
	if err != nil {
		return report, err
	}
	pages := signaturePageNumbers(doc)
	cache := make(map[int]map[string][]string)
	inv := Invoice{Kind: "inv", Standard: ofdNamespace2016, Version: "1.0", Sources: []InvoiceSource{source}}
	assigned := make(map[string]bool)
	total := 0
	var visit func(*StructureNode, string, string) error
	visit = func(node *StructureNode, nodePath, fieldPath string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.Namespace != ofdNamespace2016 {
			return nil
		}
		var refs []StructureReference
		for _, child := range node.Children {
			refs = append(refs, child.References...)
		}
		if len(refs) != 0 {
			factSource := source
			factSource.NodePath = nodePath
			fact := InvoiceFact{Name: node.Name, Namespace: node.Namespace, Source: factSource, References: refs}
			valid := true
			for _, ref := range refs {
				page, _ := resourceValue(pages, ref.PageID)
				if page != 0 && cache[page] == nil {
					content, err := r.PageContentByIndexContext(ctx, page-1)
					if err != nil {
						return err
					}
					texts := make(map[string][]string)
					var collect func(GraphicObject)
					collect = func(object GraphicObject) {
						if object.Type == "TextObject" {
							id := editorResourceID(object.TextObject.ID)
							texts[id] = append(texts[id], object.TextObject.Text())
						}
						if object.Type == "CompositeGraphicUnit" {
							for child := range object.CompositeGraphicUnit.objects() {
								collect(child)
							}
						}
					}
					for _, layer := range content.Content.Layer {
						for _, object := range layer.Objects {
							collect(object)
						}
					}
					cache[page] = texts
				}
				for _, id := range ref.ObjectIDs {
					values := cache[page][editorResourceID(id)]
					if len(values) != 1 {
						valid = false
						report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-reference", Message: "invoice text reference is missing or ambiguous", Source: factSource, Field: nodePath})
						continue
					}
					if len(values[0]) > invoiceXMLLimit-total {
						return fmt.Errorf("invoice extraction limit exceeded")
					}
					total += len(values[0])
					fact.Value += values[0]
				}
			}
			inv.Facts = append(inv.Facts, fact)
			dst, numeric := invoiceEDRMField(&inv, nil, fieldPath)
			if dst != nil && valid {
				value := strings.TrimSpace(fact.Value)
				if numeric {
					value = strings.TrimSpace(strings.TrimLeft(value, "¥￥"))
					if invoiceDecimal(value) == nil {
						report.Issues = append(report.Issues, InvoiceIssue{Code: "invalid-decimal", Message: "invalid invoice amount", Source: factSource, Field: nodePath})
						return nil
					}
				}
				if assigned[fieldPath] {
					*dst = ""
					report.Issues = append(report.Issues, InvoiceIssue{Code: "ambiguous-field", Message: "multiple invoice field references", Source: factSource, Field: nodePath})
				} else {
					*dst = value
					assigned[fieldPath] = true
				}
			}
			return nil
		}
		counts := make(map[string]int)
		for i := range node.Children {
			child := &node.Children[i]
			counts[child.Name]++
			if err := visit(child, fmt.Sprintf("%s/%s[%d]", nodePath, child.Name, counts[child.Name]), fieldPath+"/"+child.Name); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(structure, "/root[1]", "/eInvoice"); err != nil {
		return InvoiceReport{}, err
	}
	if date, err := time.Parse("2006年01月02日", inv.Date); err == nil {
		inv.Date = date.Format("2006-01-02")
	}
	invoiceAmountCheck(&inv, &report, source)
	report.Invoices = append(report.Invoices, inv)
	return report, ctx.Err()
}

// ExportInvoice 导出当前文档及版本声明的发票，内联内容补齐继承的命名空间
// 相同选择器对应多个声明时拒绝操作，允许已识别的旧数据带解析诊断
// 入参: ctx 取消上下文, source 提取结果中的精确来源
// 返回: []byte 独立XML数据, error 来源或读取错误
func (r *Reader) ExportInvoice(ctx context.Context, source InvoiceSource) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report, err := r.InvoicesContext(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, invoice := range report.Invoices {
		for _, candidate := range invoice.Sources {
			found = found || candidate == source
		}
	}
	if !found {
		return nil, fmt.Errorf("invoice source is not declared in current document")
	}
	_, data, node, _, err := r.invoiceSourceNode(ctx, source)
	if err != nil {
		return nil, err
	}
	if source.NodePath != "" {
		return editorXMLStandaloneContext(ctx, data[node.start:node.end], node)
	}
	payload, err := r.invoiceData(ctx, source.FileLoc)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(payload), nil
}

// ExportInvoice 导出编辑器当前文档及版本声明的发票数据
// 入参: ctx 取消上下文, source 提取结果中的精确来源
// 返回: []byte 独立XML数据, error 来源或读取错误
func (e *Editor) ExportInvoice(ctx context.Context, source InvoiceSource) ([]byte, error) {
	if e == nil || e.source == nil {
		return nil, fmt.Errorf("invoice source not found")
	}
	return e.source.reader.ExportInvoice(ctx, source)
}

// ReplaceInvoiceData 仅替换选中声明，独立资源写时复制，旧数据允许诊断而新数据必须无诊断
// 不删除原签名，修改签名覆盖的内容后需重新验签
// 入参: ctx 取消上下文, source 精确来源, expectedDigest 必填原数据摘要, data 新发票XML
// 返回: InvoiceSource 提交后的实际来源, error 校验、冲突或提交错误
func (e *Editor) ReplaceInvoiceData(ctx context.Context, source InvoiceSource, expectedDigest string, data []byte) (InvoiceSource, error) {
	return e.changeInvoiceSource(ctx, source, expectedDigest, data, false)
}

// DeleteInvoiceData 仅删除选中来源声明或内联发票子树，不删除资源及其他别名
// 不删除原签名，修改签名覆盖的内容后需重新验签
// 入参: ctx 取消上下文, source 精确来源, expectedDigest 必填原数据摘要
// 返回: error 校验、冲突或提交错误
func (e *Editor) DeleteInvoiceData(ctx context.Context, source InvoiceSource, expectedDigest string) error {
	_, err := e.changeInvoiceSource(ctx, source, expectedDigest, nil, true)
	return err
}

// changeInvoiceSource 校验原始摘要并原子修改声明，预检隔离后的来源供绑定重定向
// 入参: ctx 取消上下文, source 来源, digest 原始摘要, data 替换数据, remove 删除声明
// 返回: InvoiceSource 新来源, error 校验或提交错误
func (e *Editor) changeInvoiceSource(ctx context.Context, source InvoiceSource, digest string, data []byte, remove bool) (InvoiceSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if digest == "" {
		return InvoiceSource{}, fmt.Errorf("invoice digest is required")
	}
	old, err := e.ExportInvoice(ctx, source)
	if err != nil {
		return InvoiceSource{}, err
	}
	if InvoiceDigest(old) != digest {
		return InvoiceSource{}, fmt.Errorf("invoice source has changed")
	}
	if source.Kind == "tag" {
		root, err := invoiceXMLTree(ctx, old)
		if err != nil {
			return InvoiceSource{}, err
		}
		if root.name.Space == ofdNamespace2016 && root.name.Local == "root" && invoiceObjectReferences(root) {
			return InvoiceSource{}, fmt.Errorf("object reference invoice is read-only")
		}
	}
	if !remove {
		if len(data) > invoiceXMLLimit {
			return InvoiceSource{}, fmt.Errorf("invoice data limit exceeded")
		}
		if _, err := invoiceWritableReport(ctx, data); err != nil {
			return InvoiceSource{}, err
		}
		if err := invoiceValidateXMLWrite(ctx, old, data); err != nil {
			return InvoiceSource{}, err
		}
		if bytes.Equal(old, data) {
			return source, ctx.Err()
		}
	}
	base := e.source
	name, outer, selected, reference, err := base.reader.invoiceSourceNode(ctx, source)
	if err != nil {
		return InvoiceSource{}, err
	}
	parts := make(map[string][]byte)
	patch := editorXMLPatch{start: selected.start, end: selected.end}
	next := source
	if !remove && source.NodePath != "" {
		root, err := parseEditorXMLContext(ctx, data)
		if err != nil {
			return InvoiceSource{}, err
		}
		patch.data, err = editorXMLStandaloneContext(ctx, data[root.start:root.end], root)
		if err != nil {
			return InvoiceSource{}, err
		}
	} else if !remove {
		file := e.packageName("Invoices/Invoice.xml")
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte("/"+file)); err != nil {
			return InvoiceSource{}, err
		}
		patch = editorXMLContent(outer, reference, escaped.Bytes())
		parts[file] = bytes.Clone(data)
		next.FileLoc = file
	}
	parts[name] = editorPatchXML(outer, []editorXMLPatch{patch})
	if !remove && source.Kind == "attachment" {
		root, err := parseEditorXMLContext(ctx, parts[name])
		if err != nil {
			return InvoiceSource{}, err
		}
		if root.matchesOFD("Document") {
			root = root.child("Attachments")
		}
		for _, child := range root.children {
			if child.matchesOFD("Attachment") && child.attr("ID") == source.ID {
				attribute, err := editorXMLAttributePatch(parts[name], child, "Size", ofdNumber(float64(len(data))/1024))
				if err != nil {
					return InvoiceSource{}, err
				}
				parts[name] = editorPatchXML(parts[name], []editorXMLPatch{attribute})
				break
			}
		}
	}
	isolated, err := base.reader.versionChanges(ctx, parts)
	if err != nil {
		return InvoiceSource{}, err
	}
	files := make(map[string][]byte, len(base.reader.files)+len(isolated))
	for key, value := range base.reader.files {
		files[key] = value
	}
	for key, value := range isolated {
		files[key] = value
	}
	view := &Reader{Zip: base.reader.Zip, files: files, encryption: base.reader.encryption, documentIndex: base.reader.documentIndex, selectedVersion: base.reader.selectedVersion}
	if err := view.initRootContext(ctx); err != nil {
		return InvoiceSource{}, err
	}
	if !remove {
		report, err := view.InvoicesContext(ctx)
		if err != nil {
			return InvoiceSource{}, err
		}
		found := false
		for _, invoice := range report.Invoices {
			for _, candidate := range invoice.Sources {
				if candidate.Kind == next.Kind && candidate.ID == next.ID && candidate.NodePath == next.NodePath && (next.NodePath != "" || candidate.FileLoc == next.FileLoc) {
					next, found = candidate, true
				}
			}
		}
		if !found {
			return InvoiceSource{}, fmt.Errorf("replacement invoice source is not reachable")
		}
	}
	if remove && source.Kind == "tag" {
		index, err := strconv.Atoi(source.ID)
		if err != nil {
			return InvoiceSource{}, err
		}
		updated := *base
		updated.structureStarts = make(map[int]map[string]int, len(base.structureStarts))
		for entry, starts := range base.structureStarts {
			if entry < index {
				updated.structureStarts[entry] = starts
			} else if entry > index {
				updated.structureStarts[entry-1] = starts
			}
		}
		base = &updated
	}
	if err := e.commitAnnotationPartsContext(ctx, base, parts); err != nil {
		return InvoiceSource{}, err
	}
	return next, nil
}

// invoiceSourceNode 定位唯一声明及其引用节点，拒绝歧义别名，不接受任意包路径
// 入参: ctx 取消上下文, source 已提取来源
// 返回: string 声明文件, []byte 原文, *editorXML 选中声明, *editorXML 资源引用, error 定位错误
func (r *Reader) invoiceSourceNode(ctx context.Context, source InvoiceSource) (string, []byte, *editorXML, *editorXML, error) {
	doc, err := r.docStructureContext(ctx)
	if err != nil {
		return "", nil, nil, nil, err
	}
	container, entry, location := "", "", ""
	switch source.Kind {
	case "attachment":
		container, entry, location = "Attachments", "Attachment", doc.Attachments.Path
	case "tag":
		container, entry, location = "CustomTags", "CustomTag", doc.CustomTags.Path
	case "extension":
		container, entry, location = "Extensions", "Extension", doc.Extensions.Path
	default:
		return "", nil, nil, nil, fmt.Errorf("invalid invoice source kind")
	}
	name := r.documentRoot
	if strings.TrimSpace(location) != "" {
		name = r.ResPath(location)
	}
	data, err := r.invoiceData(ctx, name)
	if err != nil {
		return "", nil, nil, nil, err
	}
	root, err := parseEditorXMLContext(ctx, data)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if root.matchesOFD("Document") {
		root = root.child(container)
	}
	var selected, reference *editorXML
	count, index, declarations := 0, 0, 0
	if root != nil && root.matchesOFD(container) {
		for _, child := range root.children {
			if !child.matchesOFD(entry) {
				continue
			}
			id := strconv.Itoa(index)
			index++
			if source.Kind == "attachment" {
				id = child.attr("ID")
			}
			if id != source.ID {
				continue
			}
			declarations++
			dataIndex := 0
			for _, node := range child.children {
				if source.NodePath != "" && source.Kind == "extension" && node.matchesOFD("Data") {
					dataIndex++
					for k, payload := range node.children {
						if source.NodePath == fmt.Sprintf("/Extensions/Extension[%d]/Data[%d]/*[%d]", index, dataIndex, k+1) && source.FileLoc == name {
							selected, count = payload, count+1
						}
					}
				} else if source.NodePath == "" && (source.Kind == "extension" && node.matchesOFD("ExtendData") || source.Kind != "extension" && node.matchesOFD("FileLoc")) {
					resolved := resolveResourcePath(name, "", strings.TrimSpace(editorImportText(data, node)))
					if resolved == source.FileLoc {
						selected, reference, count = child, node, count+1
						if source.Kind == "extension" {
							selected = node
						}
					}
				}
			}
		}
	}
	if count != 1 || declarations != 1 {
		return "", nil, nil, nil, fmt.Errorf("invoice source declaration is missing or ambiguous")
	}
	return name, data, selected, reference, ctx.Err()
}
