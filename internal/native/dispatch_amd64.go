//go:build amd64 && go1.17 && !go1.28
// +build amd64,go1.17,!go1.28

/*
 * Copyright 2021 ByteDance Inc.
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

package native

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/avx2"
	"github.com/bytedance/sonic/internal/native/sse"
	"github.com/bytedance/sonic/internal/rt"
)

const MaxFrameSize uintptr = 400

var (
	S_f64toa uintptr
	S_f32toa uintptr
	S_i64toa uintptr
	S_u64toa uintptr
	S_lspace uintptr
)

var (
	S_quote   uintptr
	S_unquote uintptr
)

var (
	S_value     uintptr
	S_vstring   uintptr
	S_vnumber   uintptr
	S_vsigned   uintptr
	S_vunsigned uintptr
)

var (
	S_skip_one      uintptr
	S_skip_one_fast uintptr
	S_get_by_path   uintptr
	S_skip_array    uintptr
	S_skip_object   uintptr
	S_skip_number   uintptr
)

var __ParseWithPadding func(parser unsafe.Pointer) (ret int)

//go:nosplit
func ParseWithPadding(parser unsafe.Pointer) (ret int) {
	return __ParseWithPadding(rt.NoEscape(unsafe.Pointer(parser)))
}

func useSSE() {
	sse.Use()
	S_f64toa = sse.S_f64toa
	S_f32toa = sse.S_f32toa
	S_i64toa = sse.S_i64toa
	S_u64toa = sse.S_u64toa
	S_lspace = sse.S_lspace
	S_quote = sse.S_quote
	S_unquote = sse.S_unquote
	S_value = sse.S_value
	S_vstring = sse.S_vstring
	S_vnumber = sse.S_vnumber
	S_vsigned = sse.S_vsigned
	S_vunsigned = sse.S_vunsigned
	S_skip_one = sse.S_skip_one
	S_skip_array = sse.S_skip_array
	S_skip_object = sse.S_skip_object
	S_skip_number = sse.S_skip_number
	S_get_by_path = sse.S_get_by_path
	__ParseWithPadding = sse.F_parse_with_padding
}

func useAVX2() {
	avx2.Use()
	S_f64toa = avx2.S_f64toa
	S_f32toa = avx2.S_f32toa
	S_i64toa = avx2.S_i64toa
	S_u64toa = avx2.S_u64toa
	S_lspace = avx2.S_lspace
	S_quote = avx2.S_quote
	S_unquote = avx2.S_unquote
	S_value = avx2.S_value
	S_vstring = avx2.S_vstring
	S_vnumber = avx2.S_vnumber
	S_vsigned = avx2.S_vsigned
	S_vunsigned = avx2.S_vunsigned
	S_skip_one = avx2.S_skip_one
	S_skip_array = avx2.S_skip_array
	S_skip_object = avx2.S_skip_object
	S_skip_number = avx2.S_skip_number
	S_get_by_path = avx2.S_get_by_path
	__ParseWithPadding = avx2.F_parse_with_padding
}

func init() {
	if cpu.HasAVX2 {
		useAVX2()
	} else if cpu.HasSSE {
		useSSE()
	} else {
		panic("Unsupported CPU, lacks of AVX2 or SSE CPUID Flag. maybe it's too old to run Sonic.")
	}
}
