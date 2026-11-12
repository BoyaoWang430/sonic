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

package rt

import (
	"unsafe"

	"github.com/bytedance/sonic/option"
)

//go:nosplit
func Mem2Str(v []byte) string {
	return unsafe.String(unsafe.SliceData(v), len(v))
}

//go:nosplit
func Str2Mem(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

//go:nosplit
func IndexChar(src string, index int) unsafe.Pointer {
	return unsafe.Add(unsafe.Pointer(unsafe.StringData(src)), index)
}

//go:nosplit
func IndexByte(ptr []byte, index int) unsafe.Pointer {
	return unsafe.Add(unsafe.Pointer(unsafe.SliceData(ptr)), index)
}

func GuardSlice(buf *[]byte, n int) {
	c := cap(*buf)
	l := len(*buf)
	if c-l < n {
		c = c>>1 + n + l
		if c < 32 {
			c = 32
		}
		tmp := make([]byte, l, c)
		copy(tmp, *buf)
		*buf = tmp
	}
}

func GuardSlice2(buf []byte, n int) []byte {
	c := cap(buf)
	l := len(buf)
	if c-l < n {
		c = c>>1 + n + l
		if c < 32 {
			c = 32
		}
		tmp := make([]byte, l, c)
		copy(tmp, buf)
		buf = tmp
	}
	return buf
}

//go:nosplit
func StrPtr(s string) unsafe.Pointer {
	return unsafe.Pointer(unsafe.StringData(s))
}

//go:nosplit
func StrFrom(p unsafe.Pointer, n int64) string {
	return unsafe.String((*byte)(p), int(n))
}

//go:nosplit
func Add(ptr unsafe.Pointer, off uintptr) unsafe.Pointer {
	return unsafe.Add(ptr, off)
}

// CanSizeResue checks whether a pooled buffer of capacity cap should be reused.
func CanSizeResue(cap int) bool {
	return cap <= int(option.LimitBufferSize)
}

func StringData(s string) *byte    { return unsafe.StringData(s) }
func SliceData[T any](s []T) *T    { return unsafe.SliceData(s) }
func String(p *byte, n int) string { return unsafe.String(p, n) }

// CloneBytesNoZero returns an independent heap copy of b without pre-zeroing
// the destination slice (compiles to runtime.stringtoslicebyte -> rawbyteslice).
func CloneBytesNoZero(b []byte) []byte {
	if len(b) == 0 {
		return []byte{}
	}
	return []byte(unsafe.String(unsafe.SliceData(b), len(b)))
}
