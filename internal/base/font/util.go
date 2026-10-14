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

package font

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// finite 判断数值是否有限
// 入参: value 数值
// 返回: bool 是否有限
func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// ExtractCollectionFont 提取集合中的单个OpenType字体，保留表内容并重新计算校验和
// 入参: data 集合数据, index 字体索引
// 返回: []byte 单字体数据, error 错误信息
func ExtractCollectionFont(data []byte, index int) ([]byte, error) {
	tables, err := FileTables(data, index)
	if err != nil {
		return nil, err
	}
	if len(tables["head"]) < 12 {
		return nil, fmt.Errorf("invalid head font table")
	}
	return SerializeOTF(tables)
}

// FileTables 读取单字体或集合指定项的表目录，不复制表数据
// 入参: data 字体数据, index 字体索引
// 返回: map[string][]byte 字体表, error 错误信息
func FileTables(data []byte, index int) (map[string][]byte, error) {
	count, err := FileCount(data)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= count {
		return nil, fmt.Errorf("invalid font index %d", index)
	}
	var offset uint64
	if string(data[:4]) == "ttcf" {
		offset = uint64(binary.BigEndian.Uint32(data[12+index*4:]))
	}
	if offset > uint64(len(data)-12) {
		return nil, fmt.Errorf("invalid font offset")
	}
	header := data[offset : offset+12]
	if string(header[:4]) != "\x00\x01\x00\x00" && string(header[:4]) != "OTTO" && string(header[:4]) != "true" {
		return nil, fmt.Errorf("invalid OpenType header")
	}
	tableCount := uint64(binary.BigEndian.Uint16(header[4:]))
	offset += 12
	if tableCount == 0 || tableCount > (uint64(len(data))-offset)/16 {
		return nil, fmt.Errorf("invalid font table directory")
	}
	tables := make(map[string][]byte, tableCount)
	for i := uint64(0); i < tableCount; i++ {
		entry := data[offset+i*16 : offset+(i+1)*16]
		tag := string(entry[:4])
		start := uint64(binary.BigEndian.Uint32(entry[8:]))
		length := uint64(binary.BigEndian.Uint32(entry[12:]))
		if start > uint64(len(data)) || length > uint64(len(data))-start {
			return nil, fmt.Errorf("invalid %s font table range", tag)
		}
		if _, exists := tables[tag]; exists {
			return nil, fmt.Errorf("duplicate %s font table", tag)
		}
		tables[tag] = data[start : start+length]
	}
	return tables, nil
}

// calcTableChecksum 计算字体表校验和
// 入参: data 表数据
// 返回: uint32 校验和
func calcTableChecksum(data []byte) uint32 {
	var sum uint32
	length := len(data)
	for i := 0; i < length; i += 4 {
		if i+4 <= length {
			sum += binary.BigEndian.Uint32(data[i : i+4])
		} else {
			var val uint32
			rem := data[i:]
			for j, b := range rem {
				val |= uint32(b) << (24 - 8*j)
			}
			sum += val
		}
	}
	return sum
}

// buildHeadTable 构建head表
// 入参: unitsPerEm 每em单位数
// 返回: []byte head表数据
func buildHeadTable(unitsPerEm uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(1))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0x5F0F3CF5))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, unitsPerEm)
	binary.Write(buf, binary.BigEndian, int64(0))
	binary.Write(buf, binary.BigEndian, int64(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(-500))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, int16(2))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	return buf.Bytes()
}

// buildHheaTable 构建hhea表
// 入参: numGlyphs 字形数量
// 返回: []byte hhea表数据
func buildHheaTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(1))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, int16(800))
	binary.Write(buf, binary.BigEndian, int16(-200))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint16(1000))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(1000))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	return buf.Bytes()
}

// buildCFFMaxpTable 构建CFF轮廓使用的maxp 0.5表
// 入参: numGlyphs 字形数量
// 返回: []byte maxp表数据
func buildCFFMaxpTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00005000))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	return buf.Bytes()
}

// buildTrueTypeMaxpTable 构建TrueType轮廓使用的maxp 1.0表
// 入参: numGlyphs 字形数量
// 返回: []byte maxp表数据
func buildTrueTypeMaxpTable(numGlyphs uint16) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00010000))
	binary.Write(buf, binary.BigEndian, uint16(numGlyphs))
	for i := 0; i < 13; i++ {
		binary.Write(buf, binary.BigEndian, uint16(0))
	}
	return buf.Bytes()
}

// buildOS2Table 构建OS/2表 (使用默认Metrics)
// 返回: []byte OS/2表数据
func buildOS2Table() []byte {
	return BuildOS2TableWithMetrics(800, -200)
}

// BuildOS2TableWithMetrics 构建OS/2表
// 入参: ascender 上升部, descender 下降部
// 返回: []byte OS/2表数据
func BuildOS2TableWithMetrics(ascender, descender int16) []byte {
	os2 := new(bytes.Buffer)
	binary.Write(os2, binary.BigEndian, uint16(3))
	binary.Write(os2, binary.BigEndian, int16(500))
	binary.Write(os2, binary.BigEndian, uint16(400))
	binary.Write(os2, binary.BigEndian, uint16(5))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(50))
	binary.Write(os2, binary.BigEndian, int16(250))
	binary.Write(os2, binary.BigEndian, int16(0))
	os2.Write(make([]byte, 10))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	os2.WriteString("PfEd")
	binary.Write(os2, binary.BigEndian, uint16(0x0040))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(255))
	binary.Write(os2, binary.BigEndian, ascender)
	binary.Write(os2, binary.BigEndian, descender)
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(ascender))
	if descender < 0 {
		binary.Write(os2, binary.BigEndian, uint16(-descender))
	} else {
		binary.Write(os2, binary.BigEndian, uint16(descender))
	}
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, uint32(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, int16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	binary.Write(os2, binary.BigEndian, uint16(0))
	return os2.Bytes()
}

// buildNameTable 构建name表 (最小化)
// 返回: []byte name表数据
func buildNameTable() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(0))
	binary.Write(buf, binary.BigEndian, uint16(6))
	return buf.Bytes()
}

// BuildPostTable 构建post表 (版本3.0, 无字形名称)
// 返回: []byte post表数据
func BuildPostTable() []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.BigEndian, uint32(0x00030000))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, int16(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	binary.Write(buf, binary.BigEndian, uint32(0))
	return buf.Bytes()
}

// buildHmtxTable 构建hmtx表
// 入参: widths 宽度列表
// 返回: []byte hmtx表数据
func buildHmtxTable(widths []uint16) []byte {
	buf := new(bytes.Buffer)
	for _, w := range widths {
		binary.Write(buf, binary.BigEndian, uint16(w))
		binary.Write(buf, binary.BigEndian, int16(0))
	}
	return buf.Bytes()
}

// SerializeOTF 序列化OpenType字体结构，一次分配输出，不修改输入表数据
// 复用各表校验和计算整份字体的调整值，不重复扫描表数据
// 入参: tables 表数据映射
// 返回: []byte 完整字体数据, error 错误信息
func SerializeOTF(tables map[string][]byte) ([]byte, error) {
	if len(tables) > math.MaxUint16/16 {
		return nil, fmt.Errorf("too many font tables")
	}
	headerSize := 12 + 16*len(tables)
	size := uint64(headerSize)
	tags := make([]string, 0, len(tables))
	for tag, data := range tables {
		if len(tag) != 4 {
			return nil, fmt.Errorf("invalid font table tag %q", tag)
		}
		if tag == "head" && len(data) < 12 {
			return nil, fmt.Errorf("invalid head font table")
		}
		size += (uint64(len(data)) + 3) &^ 3
		if size > math.MaxUint32 || size > uint64(math.MaxInt) {
			return nil, fmt.Errorf("font table data too large")
		}
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	data := make([]byte, int(size))
	numTables := len(tables)
	entrySelector := 0
	for 1<<(entrySelector+1) <= numTables {
		entrySelector++
	}
	searchRange := 1 << (entrySelector + 4)
	rangeShift := numTables*16 - searchRange
	if tables["CFF "] != nil || tables["CFF2"] != nil {
		copy(data, "OTTO")
	} else {
		binary.BigEndian.PutUint32(data, 0x00010000)
	}
	binary.BigEndian.PutUint16(data[4:], uint16(numTables))
	binary.BigEndian.PutUint16(data[6:], uint16(searchRange))
	binary.BigEndian.PutUint16(data[8:], uint16(entrySelector))
	binary.BigEndian.PutUint16(data[10:], uint16(rangeShift))
	offset, adjustment := headerSize, 0
	var checksum uint32
	for i, tag := range tags {
		table := tables[tag]
		length := (len(table) + 3) &^ 3
		padded := data[offset : offset+length]
		copy(padded, table)
		if tag == "head" {
			clear(padded[8:12])
			adjustment = offset + 8
		}
		record := data[12+i*16 : 28+i*16]
		copy(record, tag)
		tableChecksum := calcTableChecksum(padded)
		checksum += tableChecksum
		binary.BigEndian.PutUint32(record[4:], tableChecksum)
		binary.BigEndian.PutUint32(record[8:], uint32(offset))
		binary.BigEndian.PutUint32(record[12:], uint32(len(table)))
		offset += length
	}
	if adjustment != 0 {
		binary.BigEndian.PutUint32(data[adjustment:], 0xB1B0AFBA-checksum-calcTableChecksum(data[:headerSize]))
	}
	return data, nil
}
