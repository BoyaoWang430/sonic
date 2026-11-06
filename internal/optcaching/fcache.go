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

package caching

import (
	"strings"
	"unsafe"

	"github.com/bytedance/sonic/internal/resolver"
	"github.com/bytedance/sonic/internal/rt"
)

type fieldCacheEntry struct {
	name     string
	n        uint32
	lo       uint32
	hi       uint32
	idxPlus1 int32
}

type FieldCache struct {
	exact           []fieldCacheEntry
	folded          []fieldCacheEntry
	mask            uint32
	caseInsensitive map[string]int
}

//go:nosplit
func strSig(s string) (n, lo, hi uint32) {
	l := len(s)
	n = uint32(l)
	p := (*rt.GoString)(unsafe.Pointer(&s)).Ptr
	if l >= 4 {
		lo = *(*uint32)(p)
		hi = *(*uint32)(unsafe.Pointer(uintptr(p) + uintptr(l-4)))
		return
	}
	if l == 0 {
		return
	}
	b0 := uint32(*(*uint8)(p))
	if l == 1 {
		lo = b0
		return
	}
	b1 := uint32(*(*uint8)(unsafe.Pointer(uintptr(p) + 1)))
	if l == 2 {
		lo = b0 | (b1 << 8)
		return
	}
	b2 := uint32(*(*uint8)(unsafe.Pointer(uintptr(p) + 2)))
	lo = b0 | (b1 << 8) | (b2 << 16)
	return
}

//go:nosplit
func hashSig(n, lo, hi uint32) uint32 {
	h := (lo ^ (hi * 0x9e3779b1)) * 0x85ebca6b
	h ^= n * 0xc2b2ae35
	return h ^ (h >> 16)
}

//go:nosplit
func foldASCII4(w uint32) uint32 {
	mask := ((w + 0x3f3f3f3f) &^ (w + 0x25252525)) & 0x80808080
	return w | (mask >> 2)
}

//go:nosplit
func foldASCII8(w uint64) uint64 {
	mask := ((w + 0x3f3f3f3f3f3f3f3f) &^ (w + 0x2525252525252525)) & 0x8080808080808080
	return w | (mask >> 2)
}

func isPureASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func NewFieldCache(fields []resolver.FieldMeta) *FieldCache {
	if len(fields) == 0 {
		return &FieldCache{}
	}
	cap := uint32(8)
	for cap < uint32(len(fields))*4 {
		cap <<= 1
	}
	mask := cap - 1
	f := &FieldCache{
		exact:           make([]fieldCacheEntry, cap),
		folded:          make([]fieldCacheEntry, cap),
		mask:            mask,
		caseInsensitive: make(map[string]int, len(fields)),
	}
	for i, field := range fields {
		name := field.Name
		n, lo, hi := strSig(name)
		idx := hashSig(n, lo, hi) & mask
		for {
			e := &f.exact[idx]
			if e.idxPlus1 == 0 || (e.n == n && e.lo == lo && e.hi == hi && e.name == name) {
				*e = fieldCacheEntry{
					name:     name,
					n:        n,
					lo:       lo,
					hi:       hi,
					idxPlus1: int32(i + 1),
				}
				break
			}
			idx = (idx + 1) & mask
		}

		lowerName := strings.ToLower(name)
		if existingIdx, ok := f.caseInsensitive[lowerName]; !ok || i < existingIdx {
			f.caseInsensitive[lowerName] = i
			if isPureASCII(lowerName) {
				ln, llo, lhi := strSig(lowerName)
				lidx := hashSig(ln, llo, lhi) & mask
				for {
					e := &f.folded[lidx]
					if e.idxPlus1 == 0 || (e.n == ln && e.lo == llo && e.hi == lhi && e.name == lowerName) {
						*e = fieldCacheEntry{
							name:     lowerName,
							n:        ln,
							lo:       llo,
							hi:       lhi,
							idxPlus1: int32(i + 1),
						}
						break
					}
					lidx = (lidx + 1) & mask
				}
			}
		}
	}
	return f
}

func (f *FieldCache) Get(name string, caseSensitive bool) int {
	if len(f.exact) == 0 {
		return -1
	}
	n, lo, hi := strSig(name)
	mask := f.mask
	idx := hashSig(n, lo, hi) & mask
	for {
		e := &f.exact[idx]
		if e.idxPlus1 == 0 {
			break
		}
		if e.n == n && e.lo == lo && e.hi == hi && (n <= 8 || e.name == name) {
			return int(e.idxPlus1) - 1
		}
		idx = (idx + 1) & mask
	}
	if !caseSensitive {
		return f.getCaseInsensitive(name, n, lo, hi)
	}
	return -1
}

func (f *FieldCache) getCaseInsensitive(name string, n, lo, hi uint32) int {
	if (lo|hi)&0x80808080 != 0 {
		goto unicode
	}
	{
		loFold := foldASCII4(lo)
		hiFold := foldASCII4(hi)
		mask := f.mask
		idx := hashSig(n, loFold, hiFold) & mask
		if n <= 8 {
			for {
				e := &f.folded[idx]
				if e.idxPlus1 == 0 {
					return -1
				}
				if e.n == n && e.lo == loFold && e.hi == hiFold {
					return int(e.idxPlus1) - 1
				}
				idx = (idx + 1) & mask
			}
		}
		p := uintptr((*rt.GoString)(unsafe.Pointer(&name)).Ptr)
		midLen := uintptr(n - 8)
		for {
			e := &f.folded[idx]
			if e.idxPlus1 == 0 {
				break
			}
			if e.n == n && e.lo == loFold && e.hi == hiFold {
				ep := uintptr((*rt.GoString)(unsafe.Pointer(&e.name)).Ptr)
				off := uintptr(4)
				matched := true
				for off+8 <= 4+midLen {
					w := *(*uint64)(unsafe.Pointer(p + off))
					if w&0x8080808080808080 != 0 {
						goto unicode
					}
					ew := *(*uint64)(unsafe.Pointer(ep + off))
					if foldASCII8(w) != ew {
						matched = false
						break
					}
					off += 8
				}
				if matched {
					for off < 4+midLen {
						b := *(*uint8)(unsafe.Pointer(p + off))
						if b >= 0x80 {
							goto unicode
						}
						if b >= 'A' && b <= 'Z' {
							b += 'a' - 'A'
						}
						if b != *(*uint8)(unsafe.Pointer(ep + off)) {
							matched = false
							break
						}
						off++
					}
					if matched {
						return int(e.idxPlus1) - 1
					}
				}
			}
			idx = (idx + 1) & mask
		}
		// Candidate missed in folded table; verify middle bytes are ASCII before returning -1.
		off := uintptr(4)
		for off+8 <= 4+midLen {
			if *(*uint64)(unsafe.Pointer(p + off))&0x8080808080808080 != 0 {
				goto unicode
			}
			off += 8
		}
		for off < 4+midLen {
			if *(*uint8)(unsafe.Pointer(p + off)) >= 0x80 {
				goto unicode
			}
			off++
		}
		return -1
	}

unicode:
	// Preserve strings.ToLower's Unicode and invalid-UTF-8 behavior.
	if idx, ok := f.caseInsensitive[strings.ToLower(name)]; ok {
		return idx
	}
	return -1
}
