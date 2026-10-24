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

//go:build !js || !wasm

package webview

import (
	"bytes"
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xiaoqidun/guigo"
	"github.com/xiaoqidun/ofdgo"
	webuiassets "github.com/xiaoqidun/ofdgo/assets/webui"
	"github.com/xiaoqidun/ofdgo/internal/build"
	"github.com/xiaoqidun/ofdgo/internal/webui"
)

// applicationID 标识原生应用，与版本及可执行文件路径无关
const applicationID = "me.aite.ofdgo"

const applicationVersionURL = "https://ofdgo.aite.me/"
const applicationDownloadURL = "https://ofdgo.aite.me/releases/"

// Host 连接桌面系统能力和共用文档业务
type Host struct {
	mu             sync.Mutex
	window         *guigo.Window
	files          *fileStore
	engines        map[string]*webui.NativeEngine
	engineStops    map[string]func() bool
	closingEngines map[string]*engineClose
	conversionPool *ofdgo.ConversionPool
	resources      *ofdgo.Resources
	cancels        map[string]context.CancelFunc
	batchResults   map[string]any
	fontOnce       sync.Once
	fonts          []map[string]any
	fontFiles      map[string]string
	fontError      error
	pendingPaths   []string
	frontendReady  bool
	closeAllowed   bool
}

// engineClose 保存并发关闭共享的完成通知及结果
type engineClose struct {
	done chan struct{}
	err  error
}

// resourceReader 保存资源读取位置及关闭操作
type resourceReader struct {
	*io.SectionReader
	close func() error
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
	cacheBytes := int64(256 << 20)
	if memory := conversionMemory; memory != nil {
		cacheBytes = max(32<<20, memory()/8)
	}
	h.resources, err = ofdgo.NewResources(ofdgo.ResourceOptions{CacheBytes: cacheBytes, AvailableMemory: conversionMemory})
	if err != nil {
		files.close()
		return err
	}
	h.conversionPool = h.resources.ConversionPool()
	defer h.close()
	bindings := make(map[string]guigo.HandlerFunc)
	for _, name := range []string{"engine.open", "engine.close", "engine.call", "engine.start", "engine.cancel", "engine.finish", "batch", "open", "save", "directory", "file", "release", "drop.release", "child", "remove", "write.begin", "write.data", "write.commit", "write.abort", "fonts", "ready", "title", "window.info", "window.drag", "window.close", "update.check", "update.open", "print.capabilities", "print.dialog", "print.printers", "print.query", "print.properties", "print"} {
		bindings["ofdgo.host."+name] = func(ctx context.Context, args any) (any, error) { return h.call(ctx, name, args) }
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
		w.Header().Set("X-OFDGo-Build-Version", build.Version)
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
	options.OnTitleBarChanged = func(info guigo.TitleBarInfo) {
		h.emit("ofdgo.window", info)
	}
	options.OnClosing = func(ctx context.Context) bool {
		h.mu.Lock()
		allowed := h.closeAllowed || !h.frontendReady
		h.mu.Unlock()
		if !allowed {
			h.emit("ofdgo.closing", nil)
		}
		return allowed
	}
	options.OnError = func(err error) {
		h.emit("ofdgo.error", err.Error())
	}
	return guigo.Run(ctx, options)
}

// Close 结束本次资源读取
// 返回: error 关闭错误
func (r *resourceReader) Close() error {
	if r.close != nil {
		return r.close()
	}
	return nil
}

// resource 将静态资源及文件直接交给宿主资源流
// 入参: r 资源请求
// 返回: *guigo.Resource 资源响应, error 读取错误
func (h *Host) resource(r *http.Request) (*guigo.Resource, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, nil
	}
	for _, name := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
		if r.Header.Get(name) != "" {
			return nil, nil
		}
	}
	var reader io.ReaderAt
	var size int64
	var close func() error
	transferred := false
	defer func() {
		if !transferred && close != nil {
			close()
		}
	}()
	if strings.HasPrefix(r.URL.Path, "/__native/data/") {
		var err error
		reader, size, close, err = h.files.resource(strings.TrimPrefix(r.URL.Path, "/__native/data/"))
		if err != nil {
			return nil, nil
		}
	} else {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		data, err := webuiassets.NativeFS.ReadFile(name)
		if err != nil {
			return nil, nil
		}
		reader, size = bytes.NewReader(data), int64(len(data))
	}
	header := make([]byte, min(size, 512))
	_, err := reader.ReadAt(header, 0)
	if err != nil && err != io.EOF {
		return nil, err
	}
	contentType := mime.TypeByExtension(filepath.Ext(r.URL.Path))
	if contentType == "" {
		contentType = http.DetectContentType(header)
	}
	headers := http.Header{"Content-Type": {contentType}, "Content-Length": {strconv.FormatInt(size, 10)}, "Cache-Control": {"no-store"}, "Accept-Ranges": {"bytes"}}
	headers.Set("X-OFDGo-Build-Version", build.Version)
	start, length, status := int64(0), size, http.StatusOK
	if value := r.Header.Get("Range"); value != "" && r.Method == http.MethodGet {
		if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
			return nil, nil
		}
		first, last, ok := strings.Cut(strings.TrimPrefix(value, "bytes="), "-")
		if !ok {
			return nil, nil
		}
		if first == "" {
			count, err := strconv.ParseInt(last, 10, 64)
			if err != nil || count <= 0 {
				return nil, nil
			}
			length = min(count, size)
			start = size - length
		} else {
			var err error
			start, err = strconv.ParseInt(first, 10, 64)
			if err != nil || start < 0 || start >= size {
				return nil, nil
			}
			end := size - 1
			if last != "" {
				end, err = strconv.ParseInt(last, 10, 64)
				if err != nil || end < start {
					return nil, nil
				}
			}
			length = min(end, size-1) - start + 1
		}
		if length == 0 {
			return nil, nil
		}
		status = http.StatusPartialContent
		headers.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, size))
		headers.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	if r.Method == http.MethodHead {
		length = 0
	}
	transferred = true
	return &guigo.Resource{Body: &resourceReader{SectionReader: io.NewSectionReader(reader, start, length), close: close}, StatusCode: status, Header: headers}, nil
}

// emit 发送当前窗口事件
// 入参: name 事件名称, value 事件数据
func (h *Host) emit(name string, value any) {
	h.mu.Lock()
	w := h.window
	h.mu.Unlock()
	if w != nil {
		_ = w.Emit(name, value)
	}
}

// openPaths 将操作系统提供的文件登记后交给界面
// 入参: names 文件路径列表
func (h *Host) openPaths(names []string) {
	files := make([]any, 0, len(names))
	for _, name := range names {
		file, err := h.files.addDrop(name)
		if err != nil {
			h.emit("ofdgo.error", err.Error())
			continue
		}
		files = append(files, file)
	}
	h.mu.Lock()
	w := h.window
	h.mu.Unlock()
	if w != nil && w.Emit("ofdgo.files", files) == nil {
		return
	}
	for _, file := range files {
		h.files.discardDrop(file.(map[string]any)["nativeFile"].(string))
	}
}

// call 执行宿主协议并保留业务结果结构
// 入参: ctx 调用上下文, name 方法名称, value 已解码参数
// 返回: any 调用结果, error 执行错误
func (h *Host) call(ctx context.Context, name string, value any) (any, error) {
	args, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("参数无效")
	}
	text := func(key string) string { value, _ := args[key].(string); return value }
	flag := func(key string) bool { value, _ := args[key].(bool); return value }
	switch name {
	case "print.capabilities", "print.dialog", "print.printers", "print.query", "print.properties", "print":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		if name == "print.capabilities" {
			return window.PrintCapabilities(ctx)
		}
		if name == "print.dialog" {
			return nil, window.ShowPrintUI(ctx, text("kind"))
		}
		if name == "print.printers" {
			return window.ListPrinters(ctx)
		}
		if name == "print.properties" {
			return nil, window.ShowPrinterProperties(ctx, text("printer"))
		}
		data, err := json.Marshal(args)
		if err != nil {
			return nil, err
		}
		var options guigo.PrintOptions
		if err := json.Unmarshal(data, &options); err != nil {
			return nil, err
		}
		profile, err := window.QueryPrinter(ctx, options)
		if err != nil {
			return nil, err
		}
		if name == "print.query" {
			return profile, nil
		}
		if text("fingerprint") == "" || text("fingerprint") != profile.Fingerprint {
			return nil, fmt.Errorf("打印机设置已变化，请重新预览")
		}
		return window.Print(ctx, options)
	case "engine.open":
		engine := webui.NewNativeEngine(h.files.resolve)
		parallelism, cacheBytes := runtime.GOMAXPROCS(0), int64(128<<20)
		if memory := conversionMemory; memory != nil {
			available := memory()
			cacheBytes = max(32<<20, available/32)
		}
		engine.RenderParallelism = parallelism
		engine.ConversionPool = h.conversionPool
		engine.Resources = h.resources
		engine.OpenWriter = h.openWriter
		engine.ReserveOutput = h.reserveOutput
		engine.CleanupOutput = h.cleanupOutput
		engine.LoadFont = h.loadFont
		id := rand.Text()
		h.mu.Lock()
		h.engines[id] = engine
		if h.engineStops == nil {
			h.engineStops = make(map[string]func() bool)
		}
		h.engineStops[id] = context.AfterFunc(guigo.PageContext(ctx), func() { _ = h.closeEngine(id) })
		h.mu.Unlock()
		return guigo.Result{Value: map[string]any{"engine": id, "parallelism": parallelism, "cacheBytes": cacheBytes}, Discard: func() { _ = h.closeEngine(id) }}, nil
	case "batch":
		return map[string]any{"parallelism": h.conversionPool.Parallelism()}, nil
	case "update.check":
		return applicationUpdate(ctx, build.Version, applicationVersionURL)
	case "update.open":
		return nil, guigo.OpenURL(applicationDownloadURL)
	case "engine.close":
		return nil, h.closeEngine(text("engine"))
	case "engine.finish":
		key := text("engine") + "/" + fmt.Sprint(args["id"])
		h.mu.Lock()
		delete(h.batchResults, key)
		h.mu.Unlock()
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
	case "engine.start":
		operationName := text("name")
		if operationName != "ofdgoConvertFile" && operationName != "ofdgoPackFiles" {
			return nil, fmt.Errorf("unsupported batch operation")
		}
		id := text("engine")
		key := id + "/" + fmt.Sprint(args["id"])
		page := guigo.PageContext(ctx)
		operation, cancel := context.WithCancel(page)
		h.mu.Lock()
		engine := h.engines[id]
		_, duplicate := h.cancels[key]
		if engine == nil || duplicate {
			h.mu.Unlock()
			cancel()
			return nil, fmt.Errorf("invalid batch operation")
		}
		h.cancels[key] = cancel
		h.mu.Unlock()
		values, _ := args["args"].([]any)
		go func() {
			defer cancel()
			defer func() { h.mu.Lock(); delete(h.cancels, key); h.mu.Unlock() }()
			result := engine.Call(operation, operationName, values, func(event any) {
				if value, ok := event.(map[string]any); ok {
					value["id"] = args["id"]
					h.emit("ofdgo.engine."+id, value)
				}
			})
			encoded := h.encode(page, result)
			h.mu.Lock()
			if page.Err() != nil || h.engines[id] != engine {
				h.mu.Unlock()
				h.discard(encoded)
				return
			}
			if h.batchResults == nil {
				h.batchResults = make(map[string]any)
			}
			h.batchResults[key] = encoded
			h.mu.Unlock()
			h.emit("ofdgo.engine."+id, map[string]any{"id": args["id"], "type": "result", "result": encoded})
		}()
		return guigo.Result{Value: true, Discard: cancel}, nil
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
		result := engine.Call(operation, text("name"), values, func(event any) {
			if value, ok := event.(map[string]any); ok {
				value["id"] = args["id"]
				h.emit("ofdgo.engine."+id, value)
			}
		})
		encoded := h.encode(guigo.PageContext(ctx), result)
		return guigo.Result{Value: encoded, Discard: func() { h.discard(encoded) }}, nil
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
	case "drop.release":
		h.files.discardDrop(text("file"))
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
		writer, err := h.files.begin(guigo.PageContext(ctx), text("handle"))
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
		if data, ok := args["data"].([]byte); ok {
			_, err := writer.Write(data)
			return nil, err
		}
		descriptor, _ := args["data"].(map[string]any)
		key, _ := descriptor["nativeFile"].(string)
		input, size, err := h.files.resolve(key)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(writer, io.NewSectionReader(input, 0, size))
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
	case "window.info", "window.drag", "window.close":
		h.mu.Lock()
		window := h.window
		h.mu.Unlock()
		if window == nil {
			return nil, fmt.Errorf("窗口尚未就绪")
		}
		if name == "window.info" {
			return window.TitleBarInfo()
		}
		if name == "window.close" {
			h.mu.Lock()
			h.closeAllowed = true
			h.mu.Unlock()
			return nil, window.Close()
		}
		values, ok := args["regions"].([]any)
		if !ok {
			return nil, fmt.Errorf("拖动区域无效")
		}
		regions := make([]guigo.Rect, 0, len(values))
		for _, value := range values {
			item, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("拖动区域无效")
			}
			x, xOK := item["x"].(float64)
			y, yOK := item["y"].(float64)
			width, widthOK := item["width"].(float64)
			height, heightOK := item["height"].(float64)
			if !xOK || !yOK || !widthOK || !heightOK {
				return nil, fmt.Errorf("拖动区域无效")
			}
			regions = append(regions, guigo.Rect{X: x, Y: y, Width: width, Height: height})
		}
		return nil, window.SetDragRegions(regions)
	case "ready":
		h.mu.Lock()
		paths := h.pendingPaths
		h.pendingPaths = nil
		h.frontendReady = true
		h.mu.Unlock()
		if len(paths) != 0 {
			h.openPaths(paths)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("未知宿主操作")
}

// applicationUpdate 比较官网和当前程序的完整构建版本
// 入参: ctx 请求上下文, version 当前版本, endpoint 官网地址
// 返回: bool 是否有更新, error 检查错误
func applicationUpdate(ctx context.Context, version, endpoint string) (bool, error) {
	if version == "" {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Cache-Control", "no-store")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("检查更新失败: %s", response.Status)
	}
	latest := response.Header.Get("X-OFDGo-Build-Version")
	if latest == "" {
		return false, errors.New("缺少构建版本")
	}
	return latest != version, nil
}

// dialogFilters 将共用界面的文件类型映射到系统对话框
// 入参: args 对话框参数
// 返回: []guigo.FileFilter 文件类型列表
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
// 入参: err 对话框错误
// 返回: error 页面可识别的错误
func dialogError(err error) error {
	if errors.Is(err, guigo.ErrCanceled) {
		return &guigo.BridgeError{Name: "AbortError", Message: "已取消"}
	}
	return err
}

// encode 将二进制输出登记为页面资源
// 入参: ctx 页面上下文, value 原生调用结果
// 返回: any 包含资源描述的调用结果
func (h *Host) encode(ctx context.Context, value any) any {
	switch v := value.(type) {
	case []byte:
		return h.files.bytes(ctx, v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = h.encode(ctx, item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = h.encode(ctx, item)
		}
		return out
	default:
		return value
	}
}

// discard 释放页面未接收的临时结果
// 入参: value 包含资源描述的调用结果
func (h *Host) discard(value any) {
	switch v := value.(type) {
	case map[string]any:
		if id, ok := v["nativeFile"].(string); ok && v["temporary"] == true {
			h.files.release(id)
			return
		}
		for _, item := range v {
			h.discard(item)
		}
	case []any:
		for _, item := range v {
			h.discard(item)
		}
	}
}

// openWriter 将授权目标转换为原生事务写入器
// 入参: ctx 调用上下文, value 目标句柄, name 输出文件名
// 返回: io.WriteCloser 事务写入器, error 创建错误
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
	return h.files.begin(ctx, id)
}

// reserveOutput 为一次转换预留独立输出文件或目录
// 入参: ctx 调用上下文, request 输出请求
// 返回: webui.NativeOutputTarget 预留目标, error 创建错误
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
// 入参: ctx 调用上下文, target 预留目标
// 返回: error 清理错误
func (h *Host) cleanupOutput(ctx context.Context, target webui.NativeOutputTarget) error {
	handle, _ := target.Destination.(map[string]any)
	id, _ := handle["nativeHandle"].(string)
	return h.files.remove(id, target.Name, true)
}

// readFonts 建立系统字体目录并登记按需读取资源
func (h *Host) readFonts() {
	platform, language := fontNameLanguage()
	for _, name := range ofdgo.FontSystemFiles() {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		faces, err := (ofdgo.FontFile{Name: filepath.Base(name), Data: data}).LocalizedFaces(platform, language)
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
			h.fonts = append(h.fonts, map[string]any{"index": face.Index, "postscriptName": face.PostScriptName, "fullName": face.FullName, "family": face.Family, "style": face.Style, "names": face.Names, "file": file})
			for _, alias := range append([]string{face.FullName, face.Family, face.PostScriptName, strings.TrimSpace(face.Family + " " + face.Style)}, face.Names...) {
				if alias != "" && h.fontFiles[alias+".ttf"] == "" {
					h.fontFiles[alias+".ttf"] = name + "\x00" + strconv.Itoa(face.Index)
				}
			}
		}
	}
}

// loadFont 按转换请求读取单个系统字体
// 入参: ctx 调用上下文, name 字体别名对应的文件名
// 返回: []byte 字体数据, error 读取错误
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

// closeEngine 取消会话调用并释放文档
// 入参: id 会话标识
// 返回: error 关闭错误
func (h *Host) closeEngine(id string) error {
	var results []any
	h.mu.Lock()
	if closing := h.closingEngines[id]; closing != nil {
		h.mu.Unlock()
		<-closing.done
		return closing.err
	}
	engine := h.engines[id]
	if engine == nil {
		h.mu.Unlock()
		return nil
	}
	closing := &engineClose{done: make(chan struct{})}
	if h.closingEngines == nil {
		h.closingEngines = make(map[string]*engineClose)
	}
	h.closingEngines[id] = closing
	delete(h.engines, id)
	if stop := h.engineStops[id]; stop != nil {
		stop()
		delete(h.engineStops, id)
	}
	for key, cancel := range h.cancels {
		if strings.HasPrefix(key, id+"/") {
			cancel()
		}
	}
	for key, result := range h.batchResults {
		if strings.HasPrefix(key, id+"/") {
			results = append(results, result)
			delete(h.batchResults, key)
		}
	}
	h.mu.Unlock()
	for _, result := range results {
		h.discard(result)
	}
	err := engine.Close()
	h.mu.Lock()
	closing.err = err
	delete(h.closingEngines, id)
	close(closing.done)
	h.mu.Unlock()
	return err
}

// close 取消任务并释放当前窗口持有的会话
func (h *Host) close() {
	h.mu.Lock()
	for _, cancel := range h.cancels {
		cancel()
	}
	engines := make([]string, 0, len(h.engines)+len(h.closingEngines))
	for id := range h.engines {
		engines = append(engines, id)
	}
	for id := range h.closingEngines {
		engines = append(engines, id)
	}
	results := h.batchResults
	h.batchResults = nil
	h.mu.Unlock()
	for _, id := range engines {
		_ = h.closeEngine(id)
	}
	for _, result := range results {
		h.discard(result)
	}
	if h.resources != nil {
		h.resources.Clear()
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
		name := "ofdgo"
		if runtime.GOOS == "windows" {
			name = "OFDGo"
		}
		dataDir = filepath.Join(cache, name)
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
		AppID:    applicationID,
		Title:    "OFDGo - OFD编辑器",
		TitleBar: guigo.TitleBarContent,
		TitleBarColors: guigo.TitleBarColors{
			Background:         "#e8edf0",
			Foreground:         "#1f2428",
			InactiveBackground: "#eef1f4",
			InactiveForeground: "#66737f",
		},
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
