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
	"runtime"
	"sync"
	"time"
)

// ConversionPoolOptions 设置共享转换并行度及图像工作集预算
// Parallelism为0时使用GOMAXPROCS，MemoryLimit为0时不指定固定预算
// AvailableMemory返回可用于新增任务的字节数，nil表示不探测，回调须支持并发调用
// 内存预算用于调度，不限制解析器及运行时的实际分配
type ConversionPoolOptions struct {
	Parallelism     int
	MemoryLimit     int64
	AvailableMemory func() int64
}

// ConversionPool 协调独立转换和图像任务，配置在创建后不变，可跨会话复用
type ConversionPool struct {
	options ConversionPoolOptions
	mu      sync.Mutex
	changed chan struct{}
	active  int
	images  int
	memory  int64
}

// conversionPoolKey 标识单次转换使用的共享预算
type conversionPoolKey struct{}

// NewConversionPool 创建共享转换调度器，不启动后台任务
// 入参: options 并行及内存选项
// 返回: *ConversionPool 调度器, error 配置错误
func NewConversionPool(options ConversionPoolOptions) (*ConversionPool, error) {
	if options.Parallelism < 0 || options.MemoryLimit < 0 {
		return nil, fmt.Errorf("invalid conversion pool options")
	}
	if options.Parallelism == 0 {
		options.Parallelism = runtime.GOMAXPROCS(0)
	}
	return &ConversionPool{options: options, changed: make(chan struct{})}, nil
}

// Parallelism 返回同时运行的转换数量上限
// 返回: int 并行上限
func (p *ConversionPool) Parallelism() int { return p.options.Parallelism }

// memoryLimit 读取当前可分配的图像工作量预算
// 返回: int64 可用字节数，负值表示未设置预算
func (p *ConversionPool) memoryLimit() int64 {
	limit := int64(-1)
	if p.options.MemoryLimit > 0 {
		limit = p.options.MemoryLimit
	}
	if p.options.AvailableMemory != nil {
		available := max(0, p.options.AvailableMemory())
		if limit < 0 || available < limit {
			limit = available
		}
	}
	return limit
}

// acquire 等待转换或图像预算，超预算图像可独占运行
// 入参: ctx 取消上下文, image 是否为图像任务, size 图像工作量
// 返回: error 取消错误
func (p *ConversionPool) acquire(ctx context.Context, image bool, size int64) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		limit := p.memoryLimit()
		p.mu.Lock()
		allowed := p.active < p.options.Parallelism && (p.active == 0 || limit != 0)
		if image {
			allowed = p.images < runtime.GOMAXPROCS(0) && (p.images == 0 || limit < 0 || size <= limit-p.memory)
		}
		if allowed {
			if image {
				p.images++
				p.memory += size
			} else {
				p.active++
			}
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
	}
}

// release 归还任务预算并唤醒等待者
// 入参: image 是否为图像任务, size 图像工作量
func (p *ConversionPool) release(image bool, size int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if image {
		p.images--
		p.memory -= size
	} else {
		p.active--
	}
	close(p.changed)
	p.changed = make(chan struct{})
}

// conversionWorkers 按当前文件数量分配内部计算任务
// 入参: ctx 转换上下文, fallback 非共享模式并行上限
// 返回: int 内部并行数
func conversionWorkers(ctx context.Context, fallback int) int {
	p, _ := ctx.Value(conversionPoolKey{}).(*ConversionPool)
	if p == nil {
		return min(fallback, runtime.GOMAXPROCS(0))
	}
	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	return max(1, runtime.GOMAXPROCS(0)/max(1, active))
}
