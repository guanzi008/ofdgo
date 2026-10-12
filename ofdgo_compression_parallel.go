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
	"image"
	"io"
	"runtime"
	"sync"
)

// outputImageBatchLimit 限制并行批次持有的原始编码，超限图片单独处理
const outputImageBatchLimit = 16 << 20

// outputImageWorkingSetLimit 限制并行批次的像素工作量估算，超限图片单独处理
const outputImageWorkingSetLimit = 64 << 20

// outputImageJob 保存独立图片任务，不访问编辑文档或调用进度回调
type outputImageJob struct {
	key       outputImageKey
	data      []byte
	candidate []byte
	err       error
}

// outputImageSource 保存已读取资源的摘要和格式，不保留原始编码
type outputImageSource struct {
	key    outputImageKey
	format string
}

// outputImageOptions 统一资源压缩及像素需求，受保护图片只进行无损编码
// 入参: key 资源路径
// 返回: CompressionOptions 实际策略, image.Point 像素需求
func (e *Editor) outputImageOptions(key string) (CompressionOptions, image.Point) {
	options := e.output.options
	if !e.output.images[key] {
		options.Mode = CompressionLossless
	}
	demand := e.output.sizes[key]
	if options.Mode != CompressionLossy {
		options = CompressionOptions{Mode: options.Mode}
		demand = image.Point{}
	}
	return options, demand
}

// prepareOutputImages 有界并行优化独立图片，读取、缓存及进度仍在调用线程处理
// 入参: keys 资源路径, load 串行读取方法，空值表示跳过, results 候选缓存, sources 原始资源摘要，可为空
// 返回: error 读取、进度或取消错误
func (e *Editor) prepareOutputImages(keys []string, load func(string) ([]byte, error), results map[outputImageKey][]byte, sources map[string]outputImageSource) error {
	workers := min(2, runtime.GOMAXPROCS(0))
	if workers < 2 || len(keys) < 2 {
		return nil
	}
	batch := make([]outputImageJob, 0, workers)
	size, workingSet := 0, 0
	run := func() error {
		if err := e.runOutputImages(batch, results); err != nil {
			return err
		}
		clear(batch)
		batch, size, workingSet = batch[:0], 0, 0
		return nil
	}
	for _, key := range keys {
		if err := e.output.ctx.Err(); err != nil {
			return err
		}
		if err := editorProgress(e.OnWriteProgress).report("compress", e.output.completed, 0); err != nil {
			return err
		}
		data, err := load(key)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			continue
		}
		options, demand := e.outputImageOptions(key)
		cacheKey := outputImageKey{hash: sha256.Sum256(data), options: options, size: demand}
		if sources != nil {
			sources[key] = outputImageSource{key: cacheKey, format: outputImageFormat(data)}
		}
		if _, cached := results[cacheKey]; cached {
			continue
		}
		duplicate := false
		for _, job := range batch {
			duplicate = duplicate || job.key == cacheKey
		}
		if duplicate {
			continue
		}
		cost := outputImageWorkingSet(e.output.ctx, data)
		if len(batch) != 0 && (len(data) > outputImageBatchLimit-size || cost > outputImageWorkingSetLimit-workingSet) {
			if err := run(); err != nil {
				return err
			}
		}
		batch = append(batch, outputImageJob{key: cacheKey, data: data})
		size += len(data)
		workingSet += cost
		if len(batch) == workers || size >= outputImageBatchLimit || workingSet >= outputImageWorkingSetLimit {
			if err := run(); err != nil {
				return err
			}
		}
	}
	return run()
}

// outputImageWorkingSet 按原图尺寸估算解码和重采样缓冲，不以降采样目标代替原图
// 按两份16位RGBA像素缓冲分批，不代表实际内存上限；格式或尺寸不明时单独处理
// 入参: ctx 取消上下文, data 原始图像编码
// 返回: int 编码及像素缓冲的估算字节数，上限为单批预算
func outputImageWorkingSet(ctx context.Context, data []byte) int {
	if len(data) >= outputImageWorkingSetLimit {
		return outputImageWorkingSetLimit
	}
	input := imageInput{ReadCloser: io.NopCloser(bytes.NewReader(data)), context: ctx}
	config, _, err := decodeImageConfig(input)
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return outputImageWorkingSetLimit
	}
	available := outputImageWorkingSetLimit - len(data)
	if config.Width > available/16/config.Height {
		return outputImageWorkingSetLimit
	}
	return len(data) + config.Width*config.Height*16
}

// runOutputImages 等待整批结束后记录更小候选，失败图片保留原始编码
// 入参: jobs 独占图片任务, results 本次写出的候选缓存
// 返回: error 取消错误
func (e *Editor) runOutputImages(jobs []outputImageJob, results map[outputImageKey][]byte) error {
	ctx := e.output.ctx
	optimize := func(job *outputImageJob) {
		job.candidate, job.err = optimizeImageResource(ctx, job.data, job.key.options, job.key.size)
	}
	if len(jobs) == 1 {
		optimize(&jobs[0])
	} else {
		var group sync.WaitGroup
		for i := range jobs {
			group.Go(func() { optimize(&jobs[i]) })
		}
		group.Wait()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, job := range jobs {
		results[job.key] = nil
		if job.err == nil && len(job.candidate) < len(job.data) {
			results[job.key] = job.candidate
		}
	}
	return nil
}
