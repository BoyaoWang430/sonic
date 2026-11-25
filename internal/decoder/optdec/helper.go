//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package optdec

import (
	"encoding/json"
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/internal/utils"
)

func SkipNumberFast(json string, start int) (int, bool) {
	// find the number ending, we parsed in native, it always valid
	pos := start
	base := unsafe.Pointer(rt.StringData(json))
	for pos+8 <= len(json) {
		w := *(*uint64)(unsafe.Add(base, pos))
		if (w&0xf0f0f0f0f0f0f0f0)|(((w+0x0606060606060606)&0xf0f0f0f0f0f0f0f0)>>4) == 0x3333333333333333 {
			pos += 8
			continue
		}
		c := json[pos]
		if c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+' || c|0x20 == 'e' {
			pos++
			continue
		}
		return pos, pos != start
	}
	for pos < len(json) {
		c := json[pos]
		if c >= '0' && c <= '9' || c == '.' || c == '-' || c == '+' || c|0x20 == 'e' {
			pos++
		} else {
			break
		}
	}
	return pos, pos != start
}

// pos is the start index of the raw
func ValidNumberFast(raw string) bool {
	ret := utils.SkipNumber(raw, 0)
	if ret < 0 {
		return false
	}

	// check trailing chars
	for ret < len(raw) {
		return false
	}

	return true
}

func SkipOneFast(json string, pos int) (string, error) {
	start := native.SkipOneFast(&json, &pos)
	if start < 0 {
		return "", error_syntax(pos, json, types.ParsingError(-start).Error())
	}

	return json[start:pos], nil
}

func ParseI64(raw string) (int64, error) {
	i64, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return i64, nil
}

// parseValidRawI64 parses an already-validated JSON number span as a signed
// 64-bit integer without copying or strconv overhead for 1..18 digit integers.
func parseValidRawI64(raw string) (int64, bool) {
	n := len(raw)
	if n == 0 {
		return 0, false
	}
	neg := raw[0] == '-'
	i := 0
	if neg {
		i = 1
	}
	digits := n - i
	if digits > 0 && digits <= 18 {
		var v uint64
		for ; i < n; i++ {
			d := raw[i] - '0'
			if d > 9 {
				return 0, false
			}
			v = v*10 + uint64(d)
		}
		if neg {
			return -int64(v), true
		}
		return int64(v), true
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// parseValidRawU64 parses an already-validated JSON number span as an unsigned
// 64-bit integer without copying or strconv overhead for 1..19 digit integers.
func parseValidRawU64(raw string) (uint64, bool) {
	n := len(raw)
	if n == 0 || raw[0] == '-' {
		return 0, false
	}
	if n <= 19 {
		var v uint64
		for i := 0; i < n; i++ {
			d := raw[i] - '0'
			if d > 9 {
				return 0, false
			}
			v = v*10 + uint64(d)
		}
		return v, true
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func ParseBool(raw string) (bool, error) {
	var b bool
	err := json.Unmarshal([]byte(raw), &b)
	if err != nil {
		return false, err
	}
	return b, nil
}

func ParseU64(raw string) (uint64, error) {
	u64, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	return u64, nil
}

func ParseF64(raw string) (float64, error) {
	f64, err := number.ParseGoFloat64(raw)
	if err != nil {
		return 0, err
	}
	return f64, nil
}

func ParseF32(raw string) (float32, error) {
	f32, err := number.ParseGoFloat32(raw)
	if err != nil {
		return 0, err
	}
	return f32, nil
}

func Unquote(raw string) (string, error) {
	var u string
	err := json.Unmarshal([]byte(raw), &u)
	if err != nil {
		return "", err
	}
	return u, nil
}
