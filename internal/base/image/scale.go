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

package image

import (
	"image"
	"image/color"
	"math"
	"slices"

	"golang.org/x/image/draw"
	"golang.org/x/image/math/f64"
)

// rasterScaleBufferLimit 限制整像素缩放器的浮点中间缓冲，超出时使用逐行采样
const rasterScaleBufferLimit = 8 << 20

// rasterAxisSample 保存目标像素及其归一化源采样权重
type rasterAxisSample struct {
	pixel   int
	first   int
	weights []float64
}

// rasterImagePixel 保存未取整或裁切的预乘颜色累积值
type rasterImagePixel struct {
	r, g, b, a float64
}

// rasterImageRow 保存正在累积的目标行
type rasterImageRow struct {
	sample rasterAxisSample
	values []rasterImagePixel
}

// rasterImageSource 为通用图片提供预乘十六位采样
type rasterImageSource struct{ image.Image }

// RGBA64At 读取通用颜色的原始预乘分量
// 入参: x 横向坐标, y 纵向坐标
// 返回: color.RGBA64 预乘颜色
func (s rasterImageSource) RGBA64At(x, y int) color.RGBA64 {
	r, g, b, a := s.At(x, y).RGBA()
	return color.RGBA64{R: uint16(r), G: uint16(g), B: uint16(b), A: uint16(a)}
}

// drawRasterAxisImage 按像素中心进行轴向缩放，逐行复用横向采样并保留镜像与小数坐标
// 入参: dst 目标图像, src 源图像, m 像素变换, op 合成操作
// 返回: bool 是否使用轴向采样
func drawRasterAxisImage(dst draw.Image, src image.Image, m [6]float64, op draw.Op) bool {
	sx, sy := math.Abs(m[0]), math.Abs(m[3])
	if m[1] != 0 || m[2] != 0 || sx == 0 || sy == 0 {
		return false
	}
	for _, v := range m {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	b := src.Bounds()
	if b.Empty() {
		return true
	}
	r := TransformBounds(b, m, dst.Bounds())
	if r.Empty() {
		return true
	}
	if float64(r.Dx())*math.Min(float64(r.Dy()), math.Ceil(4*math.Max(1, sy))+2) > rasterScaleBufferLimit/32 {
		return false
	}
	xs := rasterImageAxis(r.Min.X, r.Max.X, b.Min.X, b.Max.X, m[0], m[4])
	ys := rasterImageAxis(r.Min.Y, r.Max.Y, b.Min.Y, b.Max.Y, m[3], m[5])
	if len(xs) == 0 || len(ys) == 0 {
		return true
	}
	if m[3] < 0 {
		slices.Reverse(ys)
	}
	pixels, ok := src.(image.RGBA64Image)
	if !ok {
		pixels = rasterImageSource{src}
	}
	width := len(xs)
	horizontal := make([]rasterImagePixel, width)
	columns := make([]int, width)
	output := image.NewRGBA64(image.Rect(0, 0, width, 1))
	var active []rasterImageRow
	var free [][]rasterImagePixel
	next := 0
	for y := ys[0].first; next < len(ys) || len(active) != 0; y++ {
		if len(active) == 0 && y < ys[next].first {
			y = ys[next].first
		}
		for next < len(ys) && ys[next].first == y {
			var row []rasterImagePixel
			if n := len(free); n != 0 {
				row, free = free[n-1], free[:n-1]
				clear(row)
			} else {
				row = make([]rasterImagePixel, width)
			}
			active = append(active, rasterImageRow{sample: ys[next], values: row})
			next++
		}
		rasterImageHorizontal(horizontal, pixels, xs, y, columns)
		n := 0
		for _, row := range active {
			weight := row.sample.weights[y-row.sample.first]
			if weight != 0 {
				for x, value := range horizontal {
					p := &row.values[x]
					p.r += value.r * weight
					p.g += value.g * weight
					p.b += value.b * weight
					p.a += value.a * weight
				}
			}
			if y+1 == row.sample.first+len(row.sample.weights) {
				rasterImageWriteRow(dst, output, row.values, xs[0].pixel, row.sample.pixel, op)
				free = append(free, row.values)
			} else {
				active[n] = row
				n++
			}
		}
		active = active[:n]
	}
	return true
}

// rasterImageAxis 按连续源坐标生成CatmullRom权重，不对图像几何边界取整
// 入参: first 目标起点, end 目标终点, source 源起点, limit 源终点, scale 缩放比例, offset 平移距离
// 返回: []rasterAxisSample 轴向采样表
func rasterImageAxis(first, end, source, limit int, scale, offset float64) []rasterAxisSample {
	result := make([]rasterAxisSample, 0, end-first)
	argument := math.Min(1, math.Abs(scale))
	support := draw.CatmullRom.Support / argument
	for pixel := first; pixel < end; pixel++ {
		center := (float64(pixel) + .5 - offset) / scale
		if center < float64(source) || center >= float64(limit) {
			continue
		}
		center -= .5
		start := int(max(float64(source), math.Floor(center-support)))
		stop := int(min(float64(limit), math.Ceil(center+support)))
		weights := make([]float64, stop-start)
		total := 0.0
		for i := range weights {
			distance := math.Abs((float64(start+i) - center) * argument)
			if distance < draw.CatmullRom.Support {
				weights[i] = draw.CatmullRom.At(distance)
				total += weights[i]
			}
		}
		for i := range weights {
			weights[i] /= total
		}
		result = append(result, rasterAxisSample{pixel: pixel, first: start, weights: weights})
	}
	return result
}

// rasterImageHorizontal 按源列复用像素，保留各目标像素的权重累加顺序与中间精度
// 入参: row 横向缓冲, src 源像素, samples 采样表, y 源行坐标, columns 活动列缓冲
func rasterImageHorizontal(row []rasterImagePixel, src image.RGBA64Image, samples []rasterAxisSample, y int, columns []int) {
	clear(row)
	reversed := samples[0].first > samples[len(samples)-1].first
	start, end := samples[0].first, samples[0].first+len(samples[0].weights)
	for _, sample := range samples[1:] {
		start = min(start, sample.first)
		end = max(end, sample.first+len(sample.weights))
	}
	next, count := 0, 0
	for x := start; x < end; x++ {
		for next < len(samples) {
			index := next
			if reversed {
				index = len(samples) - next - 1
			}
			if samples[index].first > x {
				break
			}
			columns[count] = index
			count++
			next++
		}
		p := src.RGBA64At(x, y)
		n := 0
		for _, index := range columns[:count] {
			sample := samples[index]
			weight := sample.weights[x-sample.first]
			if weight != 0 {
				value := &row[index]
				value.r += float64(p.R) * weight
				value.g += float64(p.G) * weight
				value.b += float64(p.B) * weight
				value.a += float64(p.A) * weight
			}
			if x+1 < sample.first+len(sample.weights) {
				columns[n] = index
				n++
			}
		}
		count = n
	}
}

// rasterImageWriteRow 在两轴采样完成后取整并使用标准预乘合成
// 入参: dst 目标图像, output 行像素缓冲, values 累积颜色, x 横向起点, y 目标行坐标, op 合成操作
func rasterImageWriteRow(dst draw.Image, output *image.RGBA64, values []rasterImagePixel, x, y int, op draw.Op) {
	for i, value := range values {
		output.SetRGBA64(i, 0, color.RGBA64{
			R: uint16(min(max(min(value.r, value.a)+.5, 0), 65535)),
			G: uint16(min(max(min(value.g, value.a)+.5, 0), 65535)),
			B: uint16(min(max(min(value.b, value.a)+.5, 0), 65535)),
			A: uint16(min(max(value.a+.5, 0), 65535)),
		})
	}
	draw.Draw(dst, image.Rect(x, y, x+len(values), y+1), output, image.Point{}, op)
}

// DrawPixels 对轴向缩放使用可分离采样，其余变换保留通用路径
// 入参: dst 目标图像, src 源图像, m 按a、b、c、d、e、f排列的像素变换, op 合成操作
func DrawPixels(dst draw.Image, src image.Image, m [6]float64, op draw.Op) {
	b := src.Bounds()
	if dr, ok := rasterScaleBounds(b, m); ok {
		if dr.Intersect(dst.Bounds()).Empty() {
			return
		}
		draw.CatmullRom.Scale(dst, dr, src, b, op, nil)
		return
	}
	if drawRasterAxisImage(dst, src, m, op) {
		return
	}
	draw.CatmullRom.Transform(dst, f64.Aff3{m[0], m[2], m[4], m[1], m[3], m[5]}, src, b, op, nil)
}

// rasterScaleBounds 检查变换是否可无几何取整地使用缩放器，并限制其中间缓冲
// 入参: b 源边界, m 像素变换
// 返回: image.Rectangle 缩放边界, bool 是否适用
func rasterScaleBounds(b image.Rectangle, m [6]float64) (image.Rectangle, bool) {
	if b.Empty() || m[1] != 0 || m[2] != 0 || m[0] <= 0 || m[0] > 1 || m[3] <= 0 || m[3] > 1 || m[0] == 1 && m[3] == 1 {
		return image.Rectangle{}, false
	}
	x0, y0 := float64(m[0]*float64(b.Min.X))+m[4], float64(m[3]*float64(b.Min.Y))+m[5]
	x1, y1 := float64(m[0]*float64(b.Max.X))+m[4], float64(m[3]*float64(b.Max.Y))+m[5]
	for _, v := range [4]float64{x0, y0, x1, y1} {
		if v != math.Floor(v) || v < math.MinInt32 || v > math.MaxInt32 {
			return image.Rectangle{}, false
		}
	}
	if x1 <= x0 || y1 <= y0 || b.Dx() > math.MaxInt32 || b.Dy() > math.MaxInt32 || (x1-x0)*float64(b.Dy()) > rasterScaleBufferLimit/32 {
		return image.Rectangle{}, false
	}
	return image.Rect(int(x0), int(y0), int(x1), int(y1)), true
}

// TransformBounds 计算采样器可能写入的像素范围，并在整数转换前裁至目标边界
// 入参: source 原始像素边界, m 像素变换, target 目标边界
// 返回: image.Rectangle 受影响的目标像素区域
func TransformBounds(source image.Rectangle, m [6]float64, target image.Rectangle) image.Rectangle {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range [4]image.Point{source.Min, {X: source.Max.X, Y: source.Min.Y}, {X: source.Min.X, Y: source.Max.Y}, source.Max} {
		x := math.Floor(float64(m[0]*float64(p.X)) + float64(m[2]*float64(p.Y)) + m[4])
		y := math.Floor(float64(m[1]*float64(p.X)) + float64(m[3]*float64(p.Y)) + m[5])
		minX, minY = math.Min(minX, x), math.Min(minY, y)
		maxX, maxY = math.Max(maxX, x+1), math.Max(maxY, y+1)
	}
	minX, minY = math.Max(minX, float64(target.Min.X)), math.Max(minY, float64(target.Min.Y))
	maxX, maxY = math.Min(maxX, float64(target.Max.X)), math.Min(maxY, float64(target.Max.Y))
	if minX >= maxX || minY >= maxY {
		return image.Rectangle{}
	}
	return image.Rect(int(minX), int(minY), int(maxX), int(maxY))
}
