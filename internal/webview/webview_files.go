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

package webview

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// fileEntry 保存窗口授权的文件或目录
type fileEntry struct {
	path      string
	name      string
	data      []byte
	file      *os.File
	directory bool
	owned     bool
}

// fileStore 管理窗口范围内的文件授权和临时数据
type fileStore struct {
	mu        sync.Mutex
	entries   map[string]*fileEntry
	writers   map[string]*fileWriter
	temporary string
	closed    bool
}

// fileWriter 在同目录临时文件中保存输出
type fileWriter struct {
	id     string
	mu     sync.Mutex
	store  *fileStore
	file   *os.File
	target string
	err    error
	closed bool
}

// newFileStore 创建独立文件授权表
func newFileStore() (*fileStore, error) {
	dir, err := os.MkdirTemp("", "ofdgo-webview-")
	if err != nil {
		return nil, err
	}
	return &fileStore{entries: make(map[string]*fileEntry), writers: make(map[string]*fileWriter), temporary: dir}, nil
}

// add 登记已有授权文件
func (s *fileStore) add(name string, directory bool) (map[string]any, error) {
	info, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.IsDir() != directory {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.entries[id] = &fileEntry{path: name, name: filepath.Base(name), directory: directory}
	return s.descriptor(id), nil
}

// save 登记用户选择的输出路径
func (s *fileStore) save(name string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.entries[id] = &fileEntry{path: name, name: filepath.Base(name)}
	return s.descriptor(id)
}

// descriptor 返回文件句柄，不暴露本机路径
func (s *fileStore) descriptor(id string) map[string]any {
	entry := s.entries[id]
	kind := "file"
	if entry.directory {
		kind = "directory"
	}
	return map[string]any{"nativeHandle": id, "name": entry.name, "kind": kind}
}

// file 创建独立读取快照并返回资源描述
func (s *fileStore) file(id string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, fmt.Errorf("文件授权无效")
	}
	size := int64(len(entry.data))
	if entry.data == nil {
		file, err := openReadFile(entry.path)
		if err != nil {
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, err
		}
		size = info.Size()
		id = rand.Text()
		entry = &fileEntry{path: entry.path, name: entry.name, file: file}
		s.entries[id] = entry
	}
	return map[string]any{"nativeFile": id, "name": entry.name, "size": size, "url": "/__native/data/" + id}, nil
}

// resolve 按授权标识读取文件并保持随机读取句柄
func (s *fileStore) resolve(id string) (io.ReaderAt, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[id]
	if entry == nil || entry.directory {
		return nil, 0, fmt.Errorf("文件授权无效")
	}
	if entry.data != nil {
		return bytes.NewReader(entry.data), int64(len(entry.data)), nil
	}
	if entry.file == nil {
		file, err := openReadFile(entry.path)
		if err != nil {
			return nil, 0, err
		}
		entry.file = file
	}
	info, err := entry.file.Stat()
	if err != nil {
		return nil, 0, err
	}
	return entry.file, info.Size(), nil
}

// bytes 登记原生输出，前端通过资源通道读取
func (s *fileStore) bytes(data []byte) map[string]any {
	if data == nil {
		data = []byte{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := rand.Text()
	s.entries[id] = &fileEntry{data: data, name: "data"}
	return map[string]any{"nativeFile": id, "size": len(data), "name": "data", "url": "/__native/data/" + id, "temporary": true}
}

// release 释放已传递完成的内存结果
func (s *fileStore) release(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry := s.entries[id]; entry != nil && entry.data != nil {
		delete(s.entries, id)
	}
}

// child 在已授权目录中查找或创建直接子项
func (s *fileStore) child(id, name string, directory, create bool) (map[string]any, error) {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return nil, fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return nil, fmt.Errorf("目录授权无效")
	}
	path := filepath.Join(parent.path, name)
	for key, entry := range s.entries {
		if entry.path == path && entry.directory == directory {
			if _, err := os.Stat(path); err == nil {
				return s.descriptor(key), nil
			}
		}
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(name)
	created := false
	if errors.Is(err, os.ErrNotExist) && create {
		if directory {
			err = root.Mkdir(name, 0755)
		} else {
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				err = file.Close()
			}
		}
		if err != nil {
			return nil, err
		}
		created = true
		info, err = root.Stat(name)
	}
	if err != nil {
		return nil, err
	}
	if info.IsDir() != directory {
		return nil, fmt.Errorf("文件类型不匹配")
	}
	key := rand.Text()
	s.entries[key] = &fileEntry{path: filepath.Join(parent.path, name), name: name, directory: directory, owned: created}
	return s.descriptor(key), nil
}

// reserve 为批量输出独占创建入口并避开已有文件
func (s *fileStore) reserve(id, base, extension string, directory bool) (string, string, error) {
	if !fs.ValidPath(base) || base == "." || strings.ContainsAny(base+extension, "/\\:") {
		return "", "", fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return "", "", fmt.Errorf("目录授权无效")
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	for suffix := 0; ; suffix++ {
		stem := base
		if suffix > 0 {
			stem = fmt.Sprintf("%s (%d)", base, suffix+1)
		}
		name := stem
		if !directory {
			name += "." + extension
		}
		if directory {
			err = root.Mkdir(name, 0755)
		} else {
			var file *os.File
			file, err = root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				err = file.Close()
				if err != nil {
					root.Remove(name)
				}
			}
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		s.entries[rand.Text()] = &fileEntry{path: filepath.Join(parent.path, name), name: name, directory: directory, owned: true}
		return stem, name, nil
	}
}

// begin 创建文件写入事务
func (s *fileStore) begin(id string) (*fileWriter, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	entry := s.entries[id]
	if entry == nil || entry.directory || entry.path == "" {
		return nil, fmt.Errorf("输出授权无效")
	}
	file, err := os.CreateTemp(filepath.Dir(entry.path), ".ofdgo-*")
	if err != nil {
		return nil, err
	}
	writer := &fileWriter{id: rand.Text(), store: s, file: file, target: entry.path}
	s.writers[writer.id] = writer
	return writer, nil
}

// remove 清理由当前窗口创建的目录子项
func (s *fileStore) remove(id, name string, recursive bool) error {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "/\\:") {
		return fmt.Errorf("文件名无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	parent := s.entries[id]
	if parent == nil || !parent.directory {
		return fmt.Errorf("目录授权无效")
	}
	target := filepath.Join(parent.path, name)
	owned := false
	for _, entry := range s.entries {
		if entry.path == target && entry.owned {
			owned = true
			break
		}
	}
	if !owned {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("只能清理本次创建的输出")
	}
	root, err := os.OpenRoot(parent.path)
	if err != nil {
		return err
	}
	defer root.Close()
	for key, entry := range s.entries {
		if entry.path == target || recursive && strings.HasPrefix(entry.path, target+string(filepath.Separator)) {
			if entry.file != nil {
				entry.file.Close()
			}
			delete(s.entries, key)
		}
	}
	if recursive {
		return root.RemoveAll(name)
	}
	return root.Remove(name)
}

// Write 追加输出数据
func (w *fileWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	w.err = err
	return n, err
}

// Close 同步并替换目标文件
func (w *fileWriter) Close() error {
	defer w.release()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return os.ErrClosed
	}
	w.closed = true
	if w.err != nil {
		return errors.Join(w.err, w.file.Close(), os.Remove(w.file.Name()))
	}
	if err := w.file.Sync(); err != nil {
		w.file.Close()
		os.Remove(w.file.Name())
		return err
	}
	if err := w.file.Close(); err != nil {
		os.Remove(w.file.Name())
		return err
	}
	if err := replaceFile(w.file.Name(), w.target); err != nil {
		os.Remove(w.file.Name())
		return err
	}
	return nil
}

// Abort 删除未提交的临时输出
func (w *fileWriter) Abort() error {
	defer w.release()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return errors.Join(w.file.Close(), os.Remove(w.file.Name()))
}

// release 移除已结束的写入事务
func (w *fileWriter) release() {
	w.store.mu.Lock()
	defer w.store.mu.Unlock()
	delete(w.store.writers, w.id)
}

// serve 提供窗口内资源，不建立网络监听器
func (s *fileStore) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/__native/input" && r.Method == http.MethodPost {
		file, err := os.CreateTemp(s.temporary, "input-")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, err = io.Copy(file, r.Body)
		err = errors.Join(err, file.Close())
		if err != nil {
			os.Remove(file.Name())
			http.Error(w, err.Error(), 500)
			return
		}
		handle, err := s.add(file.Name(), false)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		value, err := s.file(handle["nativeHandle"].(string))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(value)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/__native/data/") || r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/__native/data/")
	reader, size, err := s.resolve(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	entry := s.entries[id]
	s.mu.Unlock()
	if entry == nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, entry.name, time.Time{}, io.NewSectionReader(reader, 0, size))
}

// close 释放授权文件与未提交输出
func (s *fileStore) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	writers := s.writers
	entries := s.entries
	s.writers = make(map[string]*fileWriter)
	s.entries = make(map[string]*fileEntry)
	s.mu.Unlock()
	var err error
	for _, writer := range writers {
		err = errors.Join(err, writer.Abort())
	}
	for _, entry := range entries {
		if entry.file != nil {
			err = errors.Join(err, entry.file.Close())
		}
	}
	return errors.Join(err, os.RemoveAll(s.temporary))
}
