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

//go:build windows && amd64

package webview

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/xiaoqidun/guigo"
	"github.com/xiaoqidun/ofdgo"
	webuiassets "github.com/xiaoqidun/ofdgo/assets/webui"
	"github.com/xiaoqidun/ofdgo/internal/webui"
)

// applicationID 标识原生应用，与版本及可执行文件路径无关
const applicationID = "me.aite.ofdgo"

// Host 连接桌面系统能力和共用文档业务
type Host struct {
	mu           sync.Mutex
	window       *guigo.Window
	files        *fileStore
	engines      map[string]*webui.NativeEngine
	cancels      map[string]context.CancelFunc
	fontOnce     sync.Once
	fonts        []map[string]any
	fontFiles    map[string]string
	fontError    error
	pendingPaths []string
}

// resourceReader 保持资源独立的读取位置，文件生命周期由窗口管理
type resourceReader struct {
	*io.SectionReader
}

// Run 启动共用WebUI的原生入口
// 入参: ctx 运行上下文, paths 初始文件路径, dataDir 浏览器数据目录
// 返回: error 启动或运行错误
func Run(ctx context.Context, paths []string, dataDir string) error {
	options, err := windowOptions(dataDir)
	if err != nil {
		return err
	}
	files, err := newFileStore()
	if err != nil {
		return err
	}
	h := &Host{files: files, engines: make(map[string]*webui.NativeEngine), cancels: make(map[string]context.CancelFunc), fontFiles: make(map[string]string), pendingPaths: append([]string(nil), paths...)}
	defer h.close()
	bindings := make(map[string]guigo.HandlerFunc)
	for _, name := range []string{"engine.open", "engine.close", "engine.call", "engine.cancel", "open", "save", "directory", "file", "release", "child", "remove", "write.begin", "write.data", "write.commit", "write.abort", "fonts", "ready", "title"} {
		bindings["ofdgo.host."+name] = func(ctx context.Context, raw json.RawMessage) (any, error) { return h.call(ctx, name, raw) }
	}
	static := http.FileServerFS(webuiassets.NativeFS)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__native/") {
			files.serve(w, r)
			return
		}
		if strings.HasSuffix(r.URL.Path, ".wasm") || strings.HasSuffix(r.URL.Path, "wasm_exec.js") || strings.HasSuffix(r.URL.Path, "ofdgo_work.js") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		static.ServeHTTP(w, r)
	})
	options.Handler = handler
	options.ResourceHandler = h.resource
	options.Bindings = bindings
	options.OnReady = func(window *guigo.Window) {
		h.mu.Lock()
		h.window = window
		h.mu.Unlock()
	}
	options.OnClose = func() {
		h.mu.Lock()
		for _, cancel := range h.cancels {
			cancel()
		}
		h.mu.Unlock()
	}
	options.OnDrop = h.openPaths
	options.OnError = func(err error) {
		h.emit("ofdgo.error", err.Error())
	}
	return guigo.Run(ctx, options)
}

// Close 结束本次资源读取
func (r *resourceReader) Close() error { return nil }

// resource 将大文件直接交给宿主资源流
func (h *Host) resource(r *http.Request) (*guigo.Resource, error) {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/__native/data/") || r.Header.Get("Range") != "" {
		return nil, nil
	}
	id := strings.TrimPrefix(r.URL.Path, "/__native/data/")
	reader, size, err := h.files.resolve(id)
	if err != nil {
		return nil, nil
	}
	header := make([]byte, min(size, 512))
	_, err = reader.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return &guigo.Resource{
		Body:   &resourceReader{io.NewSectionReader(reader, 0, size)},
		Header: http.Header{"Content-Type": {http.DetectContentType(header)}, "Content-Length": {strconv.FormatInt(size, 10)}, "Cache-Control": {"no-store"}},
	}, nil
}

// emit 发送当前窗口事件
func (h *Host) emit(name string, value any) {
	h.mu.Lock()
	w := h.window
	h.mu.Unlock()
	if w != nil {
		_ = w.Emit(name, value)
	}
}

// openPaths 将操作系统提供的文件登记后交给界面
func (h *Host) openPaths(names []string) {
	files := make([]any, 0, len(names))
	for _, name := range names {
		handle, err := h.files.add(name, false)
		if err != nil {
			h.emit("ofdgo.error", err.Error())
			continue
		}
		file, err := h.files.file(handle["nativeHandle"].(string))
		if err == nil {
			files = append(files, file)
		}
	}
	h.emit("ofdgo.files", files)
}

// call 执行宿主协议并保留业务结果结构
func (h *Host) call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	text := func(key string) string { value, _ := args[key].(string); return value }
	flag := func(key string) bool { value, _ := args[key].(bool); return value }
	switch name {
	case "engine.open":
		engine := webui.NewNativeEngine(h.files.resolve)
		engine.OpenWriter = h.openWriter
		engine.ReserveOutput = h.reserveOutput
		engine.CleanupOutput = h.cleanupOutput
		engine.LoadFont = h.loadFont
		id := rand.Text()
		h.mu.Lock()
		h.engines[id] = engine
		h.mu.Unlock()
		return map[string]any{"engine": id}, nil
	case "engine.close":
		id := text("engine")
		h.mu.Lock()
		engine := h.engines[id]
		delete(h.engines, id)
		for key, cancel := range h.cancels {
			if strings.HasPrefix(key, id+"/") {
				cancel()
			}
		}
		h.mu.Unlock()
		if engine != nil {
			return nil, engine.Close()
		}
		return nil, nil
	case "engine.cancel":
		key := text("engine") + "/" + fmt.Sprint(args["id"])
		h.mu.Lock()
		cancel := h.cancels[key]
		h.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return nil, nil
	case "engine.call":
		id := text("engine")
		key := id + "/" + fmt.Sprint(args["id"])
		operation, cancel := context.WithCancel(ctx)
		defer cancel()
		h.mu.Lock()
		engine := h.engines[id]
		if _, exists := h.cancels[key]; exists {
			h.mu.Unlock()
			return nil, fmt.Errorf("请求重复")
		}
		h.cancels[key] = cancel
		h.mu.Unlock()
		defer func() { h.mu.Lock(); delete(h.cancels, key); h.mu.Unlock() }()
		if engine == nil {
			return nil, fmt.Errorf("会话已关闭")
		}
		values, _ := args["args"].([]any)
		for i, value := range values {
			var err error
			values[i], err = nativeBytes(value)
			if err != nil {
				return nil, err
			}
		}
		result := engine.Call(operation, text("name"), values, func(event any) {
			if value, ok := event.(map[string]any); ok {
				value["id"] = args["id"]
				h.emit("ofdgo.engine."+id, value)
			}
		})
		return h.encode(result), nil
	case "open", "save", "directory":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		options := guigo.DialogOptions{DefaultName: text("suggestedName"), Filters: dialogFilters(args)}
		if name == "open" {
			var names []string
			var err error
			if flag("directory") {
				var directory string
				directory, err = window.OpenDirectory(options)
				if err == nil {
					err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
						if walkErr != nil {
							return walkErr
						}
						if err := ctx.Err(); err != nil {
							return err
						}
						if entry.Type().IsRegular() {
							names = append(names, path)
						}
						return nil
					})
				}
			} else if flag("multiple") {
				names, err = window.OpenFiles(options)
			} else {
				var file string
				file, err = window.OpenFile(options)
				if err == nil {
					names = []string{file}
				}
			}
			if err != nil {
				return nil, dialogError(err)
			}
			result := make([]any, 0, len(names))
			for _, file := range names {
				handle, err := h.files.add(file, false)
				if err != nil {
					return nil, err
				}
				value, err := h.files.file(handle["nativeHandle"].(string))
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
			return result, nil
		}
		if name == "save" {
			file, err := window.SaveFile(options)
			if err != nil {
				return nil, dialogError(err)
			}
			return h.files.save(file), nil
		}
		file, err := window.OpenDirectory(options)
		if err != nil {
			return nil, dialogError(err)
		}
		return h.files.add(file, true)
	case "file":
		return h.files.file(text("handle"))
	case "release":
		h.files.release(text("file"))
		return nil, nil
	case "child":
		value, err := h.files.child(text("handle"), text("name"), flag("directory"), flag("create"))
		if errors.Is(err, os.ErrNotExist) {
			return nil, &guigo.BridgeError{Name: "NotFoundError", Message: err.Error()}
		}
		return value, err
	case "remove":
		return nil, h.files.remove(text("handle"), text("name"), flag("recursive"))
	case "write.begin":
		writer, err := h.files.begin(text("handle"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"writer": writer.id}, nil
	case "write.data", "write.commit", "write.abort":
		id := text("writer")
		h.files.mu.Lock()
		writer := h.files.writers[id]
		h.files.mu.Unlock()
		if writer == nil {
			return nil, fmt.Errorf("写入已结束")
		}
		if name == "write.commit" {
			return nil, writer.Close()
		}
		if name == "write.abort" {
			return nil, writer.Abort()
		}
		descriptor, _ := args["data"].(map[string]any)
		if value, err := nativeBytes(descriptor); err != nil {
			return nil, err
		} else if data, ok := value.([]byte); ok {
			_, err = writer.Write(data)
			return nil, err
		}
		key, _ := descriptor["nativeFile"].(string)
		source, size, err := h.files.resolve(key)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(writer, io.NewSectionReader(source, 0, size))
		return nil, err
	case "fonts":
		h.fontOnce.Do(h.readFonts)
		return h.fonts, h.fontError
	case "title":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, nil
		}
		return nil, window.SetTitle(text("title"))
	case "ready":
		h.mu.Lock()
		paths := h.pendingPaths
		h.pendingPaths = nil
		h.mu.Unlock()
		if len(paths) != 0 {
			h.openPaths(paths)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("未知宿主操作")
}

// nativeBytes 还原仅在消息内传递的小型二进制参数
func nativeBytes(value any) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		if raw, exists := value["nativeBytes"]; exists {
			items, ok := raw.([]any)
			if !ok || len(value) != 1 || len(items) > 1<<20 {
				return nil, fmt.Errorf("二进制参数无效")
			}
			data := make([]byte, len(items))
			for i, item := range items {
				number, ok := item.(float64)
				if !ok || number < 0 || number > 255 || float64(byte(number)) != number {
					return nil, fmt.Errorf("二进制参数无效")
				}
				data[i] = byte(number)
			}
			return data, nil
		}
		for key, item := range value {
			decoded, err := nativeBytes(item)
			if err != nil {
				return nil, err
			}
			value[key] = decoded
		}
		return value, nil
	case []any:
		for i, item := range value {
			decoded, err := nativeBytes(item)
			if err != nil {
				return nil, err
			}
			value[i] = decoded
		}
		return value, nil
	default:
		return value, nil
	}
}

// dialogFilters 将共用界面的文件类型映射到系统对话框
func dialogFilters(args map[string]any) []guigo.FileFilter {
	patterns := func(values []string) string {
		var result []string
		seen := make(map[string]bool)
		for _, value := range values {
			value = strings.ToLower(strings.TrimSpace(value))
			if !strings.HasPrefix(value, ".") || strings.ContainsAny(value, "/\\;*?\x00") || seen[value] {
				continue
			}
			seen[value] = true
			result = append(result, "*"+value)
		}
		return strings.Join(result, ";")
	}
	var filters []guigo.FileFilter
	if accept, ok := args["accept"].(string); ok {
		var extensions []string
		for _, value := range strings.Split(accept, ",") {
			value = strings.ToLower(strings.TrimSpace(value))
			if strings.HasPrefix(value, ".") {
				extensions = append(extensions, value)
				continue
			}
			switch value {
			case "image/*":
				extensions = append(extensions, ".png", ".jpg", ".jpeg", ".bmp", ".tif", ".tiff", ".gif", ".webp", ".jb2", ".jbig2")
			case "font/*":
				extensions = append(extensions, ".ttf", ".otf", ".ttc", ".otc")
			default:
				values, err := mime.ExtensionsByType(value)
				if err == nil {
					sort.Strings(values)
					extensions = append(extensions, values...)
				}
			}
		}
		if pattern := patterns(extensions); pattern != "" {
			filters = append(filters, guigo.FileFilter{Name: "支持的文件", Pattern: pattern})
		}
	}
	types, _ := args["types"].([]any)
	for _, item := range types {
		value, _ := item.(map[string]any)
		accept, _ := value["accept"].(map[string]any)
		var extensions []string
		var names []string
		for name := range accept {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			values := accept[name]
			list, _ := values.([]any)
			for _, extension := range list {
				if text, ok := extension.(string); ok {
					extensions = append(extensions, text)
				}
			}
		}
		if pattern := patterns(extensions); pattern != "" {
			name, _ := value["description"].(string)
			if name == "" {
				name = pattern
			}
			filters = append(filters, guigo.FileFilter{Name: name, Pattern: pattern})
		}
	}
	return append(filters, guigo.FileFilter{Name: "所有文件", Pattern: "*.*"})
}

// dialogError 将用户取消与业务错误区分
func dialogError(err error) error {
	if errors.Is(err, guigo.ErrCanceled) {
		return &guigo.BridgeError{Name: "AbortError", Message: "已取消"}
	}
	return err
}

// encode 将二进制输出转为窗口内资源
func (h *Host) encode(value any) any {
	switch v := value.(type) {
	case []byte:
		return h.files.bytes(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = h.encode(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = h.encode(item)
		}
		return out
	default:
		return value
	}
}

// openWriter 将授权目标转换为原生事务写入器
func (h *Host) openWriter(ctx context.Context, value any, name string) (io.WriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, _ := value.(map[string]any)
	id, _ := handle["nativeHandle"].(string)
	h.files.mu.Lock()
	entry := h.files.entries[id]
	directory := entry != nil && entry.directory
	h.files.mu.Unlock()
	if directory {
		parts := strings.Split(strings.ReplaceAll(name, "\\", "/"), "/")
		for i, part := range parts {
			child, err := h.files.child(id, part, i < len(parts)-1, true)
			if err != nil {
				return nil, err
			}
			id = child["nativeHandle"].(string)
		}
		h.files.mu.Lock()
		owned := h.files.entries[id].owned
		h.files.mu.Unlock()
		if !owned {
			return nil, fmt.Errorf("输出文件已存在")
		}
	}
	return h.files.begin(id)
}

// reserveOutput 为一次转换预留独立输出文件或目录
func (h *Host) reserveOutput(ctx context.Context, request webui.NativeOutputRequest) (webui.NativeOutputTarget, error) {
	if err := ctx.Err(); err != nil {
		return webui.NativeOutputTarget{}, err
	}
	handle, _ := request.Destination.(map[string]any)
	id, _ := handle["nativeHandle"].(string)
	base, name, err := h.files.reserve(id, request.Base, request.Extension, request.Paged)
	return webui.NativeOutputTarget{Destination: request.Destination, Base: base, Name: name}, err
}

// cleanupOutput 回滚本次转换创建的输出入口
func (h *Host) cleanupOutput(ctx context.Context, target webui.NativeOutputTarget) error {
	handle, _ := target.Destination.(map[string]any)
	id, _ := handle["nativeHandle"].(string)
	return h.files.remove(id, target.Name, true)
}

// readFonts 建立系统字体目录并登记按需读取资源
func (h *Host) readFonts() {
	for _, name := range ofdgo.FontSystemFiles() {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		faces, err := (ofdgo.FontFile{Name: filepath.Base(name), Data: data}).Faces()
		if err != nil {
			continue
		}
		handle, err := h.files.add(name, false)
		if err != nil {
			continue
		}
		file, err := h.files.file(handle["nativeHandle"].(string))
		if err != nil {
			continue
		}
		for _, face := range faces {
			h.fonts = append(h.fonts, map[string]any{"index": face.Index, "postscriptName": face.PostScriptName, "fullName": face.FullName, "family": face.Family, "style": face.Style, "file": file})
			for _, alias := range []string{face.FullName, face.Family, face.PostScriptName, strings.TrimSpace(face.Family + " " + face.Style)} {
				if alias != "" {
					h.fontFiles[alias+".ttf"] = name + "\x00" + strconv.Itoa(face.Index)
				}
			}
		}
	}
}

// loadFont 按转换请求读取单个系统字体
func (h *Host) loadFont(ctx context.Context, name string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.fontOnce.Do(h.readFonts)
	parts := strings.Split(h.fontFiles[name], "\x00")
	if len(parts) != 2 {
		return nil, fmt.Errorf("系统字体不可用")
	}
	data, err := os.ReadFile(parts[0])
	if err != nil {
		return nil, err
	}
	index, _ := strconv.Atoi(parts[1])
	return (ofdgo.FontFile{Data: data}).Face(index)
}

// close 取消任务并释放当前窗口持有的会话
func (h *Host) close() {
	h.mu.Lock()
	for _, cancel := range h.cancels {
		cancel()
	}
	engines := h.engines
	h.engines = make(map[string]*webui.NativeEngine)
	h.mu.Unlock()
	for _, engine := range engines {
		_ = engine.Close()
	}
	_ = h.files.close()
}

// windowOptions 创建原生窗口配置
// 入参: dataDir 自定义浏览器数据目录
// 返回: guigo.Options 窗口配置, error 配置错误
func windowOptions(dataDir string) (guigo.Options, error) {
	if dataDir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return guigo.Options{}, err
		}
		dataDir = filepath.Join(cache, "OFDGo")
	}
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return guigo.Options{}, err
	}
	icon, err := webuiassets.NativeFS.ReadFile("ofdgo.png")
	if err != nil {
		return guigo.Options{}, err
	}
	return guigo.Options{
		AppID:       applicationID,
		Title:       "OFDGo",
		Icon:        icon,
		Width:       1440,
		Height:      900,
		MinWidth:    360,
		MinHeight:   240,
		Center:      true,
		Maximized:   true,
		UserDataDir: dataDir,
	}, nil
}
