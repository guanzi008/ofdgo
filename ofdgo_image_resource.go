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
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"sync"
	"sync/atomic"
)

// imageCacheLimit 限制图片缓存的编码数据及预计解码内存
const imageCacheLimit = 64 << 20

// EncodedImage 保留PNG或JPEG原始编码，首次读取像素时解码
// Bytes返回只读数据，Image返回共享只读像素，解码错误由Image返回
type EncodedImage struct {
	data    []byte
	format  string
	config  image.Config
	result  atomic.Pointer[encodedImageResult]
	mu      sync.Mutex
	pending chan struct{}
}

// encodedImageResult 保存已完成的像素解码结果，不保存取消或超时错误
type encodedImageResult struct {
	image image.Image
	err   error
}

// NewEncodedImage 读取PNG或JPEG头信息，不解码像素，复制输入数据
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func NewEncodedImage(data []byte) (*EncodedImage, error) {
	return newEncodedImage(bytes.Clone(data))
}

// Bytes 返回只读原始编码，调用方不得修改
// 返回: []byte 原始编码
func (img *EncodedImage) Bytes() []byte { return img.data }

// MIME 返回图片媒体类型
// 返回: string 媒体类型
func (img *EncodedImage) MIME() string { return "image/" + img.format }

// ColorModel 返回头信息中的颜色模型，不解码像素
// 返回: color.Model 颜色模型
func (img *EncodedImage) ColorModel() color.Model { return img.config.ColorModel }

// Bounds 返回图片范围，不解码像素
// 返回: image.Rectangle 图片范围
func (img *EncodedImage) Bounds() image.Rectangle {
	return image.Rect(0, 0, img.config.Width, img.config.Height)
}

// Image 首次解码图片并缓存结果或错误，支持并发读取
// 返回: image.Image 只读像素, error 解码错误
func (img *EncodedImage) Image() (image.Image, error) {
	if result := img.result.Load(); result != nil {
		return result.image, result.err
	}
	return img.ImageContext(context.Background())
}

// ImageContext 解码并共享只读像素，取消或超时后可重试，nil使用后台上下文
// 并发调用等待正在进行的解码，等待方取消不影响其他调用
// 入参: ctx 取消上下文
// 返回: image.Image 只读像素, error 解码或取消错误
func (img *EncodedImage) ImageContext(ctx context.Context) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if result := img.result.Load(); result != nil {
			return result.image, result.err
		}
		img.mu.Lock()
		if img.result.Load() != nil {
			img.mu.Unlock()
			continue
		}
		pending := img.pending
		if pending == nil {
			pending = make(chan struct{})
			img.pending = pending
			img.mu.Unlock()
			return img.decodeContext(ctx, pending)
		}
		img.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-pending:
		}
	}
}

// decodeContext 执行本次解码，退出时唤醒等待方，只缓存完整结果及稳定错误
// 入参: ctx 取消上下文, pending 本次解码的完成通知
// 返回: image.Image 只读像素, error 解码或取消错误
func (img *EncodedImage) decodeContext(ctx context.Context, pending chan struct{}) (image.Image, error) {
	defer func() {
		img.mu.Lock()
		img.pending = nil
		close(pending)
		img.mu.Unlock()
	}()
	pixels, _, err := image.Decode(imageInput{ReadCloser: io.NopCloser(bytes.NewReader(img.data)), context: ctx})
	if cancelErr := ctx.Err(); cancelErr != nil {
		return nil, cancelErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	img.result.Store(&encodedImageResult{image: pixels, err: err})
	return pixels, err
}

// At 返回像素颜色，解码失败时返回透明色，绘制前应通过Image检查错误
// 入参: x 横坐标, y 纵坐标
// 返回: color.Color 像素颜色
func (img *EncodedImage) At(x, y int) color.Color {
	decoded, err := img.Image()
	if err != nil {
		return color.Transparent
	}
	return decoded.At(x, y)
}

// ImageResource 按资源标识读取只读图片，PNG和JPEG保留原始编码并惰性解码
// 应先读取源页面以加载页面资源，返回EncodedImage时通过Image检查像素解码错误
// 入参: id 图片资源标识
// 返回: image.Image 图片资源, error 读取错误
func (r *Renderer) ImageResource(id string) (image.Image, error) {
	path, ok := resourceValue(r.Reader.ResMap, id)
	if !ok {
		return nil, fmt.Errorf("image resource %q not found", id)
	}
	return r.cachedImageResource(path)
}

// imageObjectResource 读取主图，无法读取或解码时使用对象声明的替换图
// 入参: object 图像对象
// 返回: image.Image 图片资源, error 读取或解码错误
func (r *Renderer) imageObjectResource(object ImageObject) (image.Image, error) {
	read := func(id string) (image.Image, error) {
		img, err := r.ImageResource(id)
		if err == nil && (r.decodeImages || object.Substitution != "") {
			pixels, decodeErr := imagePixelDataContext(r.pageContext, img)
			err = decodeErr
			if r.decodeImages {
				img = pixels
			}
		}
		return img, err
	}
	img, err := read(object.ResourceID)
	if err == nil || object.Substitution == "" || sameResourceID(object.Substitution, object.ResourceID) {
		return img, err
	}
	if r.pageContext != nil && r.pageContext.Err() != nil {
		return nil, r.pageContext.Err()
	}
	if r.OutputContext != nil && r.OutputContext.Err() != nil {
		return nil, r.OutputContext.Err()
	}
	img, substituteErr := read(object.Substitution)
	if substituteErr != nil {
		return nil, fmt.Errorf("image resource %q: %w; substitution %q: %w", object.ResourceID, err, object.Substitution, substituteErr)
	}
	return img, nil
}

// imageObjectMask 校验原图蒙版，替换图分辨率不同时按像素中心进行最近邻采样
// 入参: object 图片对象, target 实际绘制图像
// 返回: image.Image 二值蒙版，无蒙版时为nil, error 资源或蒙版错误
func (r *Renderer) imageObjectMask(object ImageObject, target image.Image) (image.Image, error) {
	if object.ImageMask == "" {
		return nil, nil
	}
	mask, err := r.ImageResource(object.ImageMask)
	if err != nil {
		return nil, err
	}
	size := target.Bounds().Size()
	if object.Substitution == "" {
		if mask.Bounds().Size() == size {
			return mask, nil
		}
		return nil, fmt.Errorf("image mask dimensions differ from source")
	}
	original, err := r.ImageResource(object.ResourceID)
	if err != nil {
		if mask.Bounds().Size() == size {
			return mask, nil
		}
		return nil, fmt.Errorf("cannot verify image mask dimensions: %w", err)
	}
	if mask.Bounds().Size() != original.Bounds().Size() {
		return nil, fmt.Errorf("image mask dimensions differ from source")
	}
	if mask.Bounds().Size() == size {
		return mask, nil
	}
	ctx := r.pageContext
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateImageMask(ctx, mask, original.Bounds().Size()); err != nil {
		return nil, err
	}
	mask, err = imagePixelDataContext(ctx, mask)
	if err != nil {
		return nil, err
	}
	bounds := mask.Bounds()
	result := image.NewGray(image.Rectangle{Max: size})
	for y := range size.Y {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sy := bounds.Min.Y + int((int64(y)*2+1)*int64(bounds.Dy())/(2*int64(size.Y)))
		for x := range size.X {
			sx := bounds.Min.X + int((int64(x)*2+1)*int64(bounds.Dx())/(2*int64(size.X)))
			value, err := imageMaskAlpha(mask, sx, sy)
			if err != nil {
				return nil, err
			}
			result.SetGray(x, y, color.Gray{Y: value})
		}
	}
	return result, ctx.Err()
}

// newEncodedImage 接管编码数据并读取头信息
// 入参: data 原始编码
// 返回: *EncodedImage 惰性图片, error 头信息或格式错误
func newEncodedImage(data []byte) (*EncodedImage, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if format != "jpeg" && format != "png" {
		return nil, fmt.Errorf("unsupported encoded image %q", format)
	}
	return &EncodedImage{data: data, format: format, config: config}, nil
}

// cachedImageResource 复用原始图片资源，不依赖输出后端
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取错误
func (r *Renderer) cachedImageResource(resPath string) (image.Image, error) {
	if r.pageContext != nil {
		if err := r.pageContext.Err(); err != nil {
			return nil, err
		}
	}
	resPath = cleanPackagePath(r.Reader.ResPath(resPath))
	img, ok := r.imageCache.get(resPath)
	if !ok {
		var err error
		img, err = r.readImageResource(resPath)
		if err != nil {
			return nil, err
		}
		r.imageCache.put(resPath, img, imageResourceCost(img, r.imageCache.limit))
	}
	return img, nil
}

// imageResourceCost 预留十六位像素空间，并计入编码、像素缓冲及补边源图的可见容量
// 入参: img 图片资源, limit 缓存预算
// 返回: int 估算字节数或超预算标记
func imageResourceCost(img image.Image, limit int) int {
	cost := 256
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if limit < cost || w <= 0 || h <= 0 || w > (limit-cost)/8/h {
		return limit + 1
	}
	pixels := w * h * 8
	if encoded, ok := img.(*EncodedImage); ok {
		if cap(encoded.data) > limit-cost-pixels {
			return limit + 1
		}
		return cost + pixels + cap(encoded.data)
	}
	var buffers [4]int
	switch source := img.(type) {
	case *image.RGBA:
		buffers[0] = cap(source.Pix)
	case *image.RGBA64:
		buffers[0] = cap(source.Pix)
	case *image.NRGBA:
		buffers[0] = cap(source.Pix)
	case *image.NRGBA64:
		buffers[0] = cap(source.Pix)
	case *image.Gray:
		buffers[0] = cap(source.Pix)
	case *image.Gray16:
		buffers[0] = cap(source.Pix)
	case *image.Alpha:
		buffers[0] = cap(source.Pix)
	case *image.Alpha16:
		buffers[0] = cap(source.Pix)
	case *image.CMYK:
		buffers[0] = cap(source.Pix)
	case *image.Paletted:
		if cap(source.Palette) > (limit-cost)/16 {
			return limit + 1
		}
		buffers[0], buffers[1] = cap(source.Pix), cap(source.Palette)*16
	case *image.YCbCr:
		buffers = [4]int{cap(source.Y), cap(source.Cb), cap(source.Cr)}
	case *image.NYCbCrA:
		buffers = [4]int{cap(source.Y), cap(source.Cb), cap(source.Cr), cap(source.A)}
	case *transparentEdgeImage:
		buffers[0] = imageResourceCost(source.source, limit-cost)
	case *transparentEdgeImage64:
		buffers[0] = imageResourceCost(source.source, limit-cost)
	case *rasterPaddedImage:
		buffers[0] = imageResourceCost(source.source, limit-cost)
	}
	storage := 0
	for _, size := range buffers {
		if size > limit-cost-storage {
			return limit + 1
		}
		storage += size
	}
	return cost + max(pixels, storage)
}

// decodeImageResource 读取或复用图片资源，按输出需要解码像素
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取或解码错误
func (r *Renderer) decodeImageResource(resPath string) (image.Image, error) {
	img, err := r.cachedImageResource(resPath)
	if err != nil {
		return nil, err
	}
	if r.decodeImages {
		return imagePixelDataContext(r.pageContext, img)
	}
	return img, nil
}

// readImageResource 按页面上下文读取图片，借用已加载的只读编码
// 入参: resPath 图片资源路径
// 返回: image.Image 图片对象, error 读取或解码错误
func (r *Renderer) readImageResource(resPath string) (image.Image, error) {
	ctx := r.pageContext
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := r.Reader.readFileViewContext(ctx, resPath)
	if err != nil {
		return nil, err
	}
	img, _, err := decodeImageDataContext(ctx, data)
	return img, err
}
