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

	"github.com/bytedance/sonic/internal/resolver"
)

type fieldCacheEntry struct {
	name     string
	n        uint32
	lo       uint32
	hi       uint32
	idxPlus1 int32
}

// FieldCache is a read-only struct field lookup index constructed once during
// plan compilation. Both exact and case-folded lookup tables are populated
// eagerly in NewFieldCache so published plans perform zero runtime mutations.
type FieldCache struct {
	exact           []fieldCacheEntry
	folded          []fieldCacheEntry
	caseInsensitive map[string]int
	mask            uint32
	lenMask         uint64
	foldFirst0      uint64
	foldFirst1      uint64
	foldLast0       uint64
	foldLast1       uint64
	allASCII        bool
}

func readU32LE(s string, off int) uint32 {
	_ = s[off+3]
	return uint32(s[off]) | uint32(s[off+1])<<8 | uint32(s[off+2])<<16 | uint32(s[off+3])<<24
}

func readU64LE(s string, off int) uint64 {
	_ = s[off+7]
	return uint64(s[off]) | uint64(s[off+1])<<8 | uint64(s[off+2])<<16 | uint64(s[off+3])<<24 |
		uint64(s[off+4])<<32 | uint64(s[off+5])<<40 | uint64(s[off+6])<<48 | uint64(s[off+7])<<56
}

//go:nosplit
func strSig(s string) (n, lo, hi uint32) {
	l := len(s)
	n = uint32(l)
	if l == 0 {
		return
	}
	if l >= 4 {
		lo = readU32LE(s, 0)
		hi = readU32LE(s, l-4)
		return
	}
	b0 := uint32(s[0])
	if l == 1 {
		lo = b0
		return
	}
	b1 := uint32(s[1])
	if l == 2 {
		lo = b0 | (b1 << 8)
		return
	}
	b2 := uint32(s[2])
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
	l := len(s)
	off := 0
	for off+8 <= l {
		if readU64LE(s, off)&0x8080808080808080 != 0 {
			return false
		}
		off += 8
	}
	for off < l {
		if s[off] >= 0x80 {
			return false
		}
		off++
	}
	return true
}

//go:nosplit
func equalMiddle(a, b string, n uint32) bool {
	if n <= 8 {
		return true
	}
	if n <= 12 {
		return readU32LE(a, 4) == readU32LE(b, 4)
	}
	if n <= 16 {
		return readU64LE(a, 4) == readU64LE(b, 4)
	}
	if n <= 24 {
		last := int(n) - 12
		return readU64LE(a, 4) == readU64LE(b, 4) &&
			readU64LE(a, last) == readU64LE(b, last)
	}
	return a == b
}

func foldASCIIByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// NewFieldCache constructs an immutable FieldCache with both exact and
// case-folded indices eagerly built. Exact matches take priority over folded
// matches, and folded collisions resolve to the earliest field in fields.
func NewFieldCache(fields []resolver.FieldMeta) *FieldCache {
	if len(fields) == 0 {
		return &FieldCache{allASCII: true}
	}
	tableCap := uint32(8)
	for tableCap < uint32(len(fields))*4 {
		tableCap <<= 1
	}
	mask := tableCap - 1
	f := &FieldCache{
		exact:           make([]fieldCacheEntry, tableCap),
		folded:          make([]fieldCacheEntry, tableCap),
		caseInsensitive: make(map[string]int, len(fields)),
		mask:            mask,
		allASCII:        true,
	}

	// Track canonical exact winners first (in case fields ever repeats an exact name,
	// matching exact-table overwrite semantics before building folded entries).
	canonical := make([]bool, len(fields))
	exactSeen := make(map[string]int, len(fields))
	for i, field := range fields {
		if prev, ok := exactSeen[field.Name]; ok {
			canonical[prev] = false
		}
		exactSeen[field.Name] = i
		canonical[i] = true
	}

	for i, field := range fields {
		name := field.Name
		l := len(name)
		bit := uint(l)
		if bit > 63 {
			bit = 63
		}
		f.lenMask |= uint64(1) << bit
		if !isPureASCII(name) {
			f.allASCII = false
		} else if l > 0 {
			fb := foldASCIIByte(name[0])
			if fb < 64 {
				f.foldFirst0 |= uint64(1) << fb
			} else {
				f.foldFirst1 |= uint64(1) << (fb - 64)
			}
			lb := foldASCIIByte(name[l-1])
			if lb < 64 {
				f.foldLast0 |= uint64(1) << lb
			} else {
				f.foldLast1 |= uint64(1) << (lb - 64)
			}
		}

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

		if !canonical[i] {
			continue
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

// Get is the sole authoritative field lookup entry for all struct decoders.
// Exact matches always win; when caseSensitive is false, folded lookup preserves
// strings.ToLower semantics and first-declared conflict precedence.
func (f *FieldCache) Get(name string, caseSensitive bool) int {
	exact := f.exact
	if len(exact) == 0 {
		return -1
	}
	n, lo, hi := strSig(name)
	mask := uint32(len(exact) - 1)
	idx := hashSig(n, lo, hi) & mask
	for {
		e := &exact[idx]
		if e.idxPlus1 == 0 {
			break
		}
		if e.n == n && e.lo == lo && e.hi == hi && equalMiddle(name, e.name, n) {
			return int(e.idxPlus1) - 1
		}
		idx = (idx + 1) & mask
	}
	if caseSensitive {
		return -1
	}
	bit := uint(n)
	if bit > 63 {
		bit = 63
	}
	if (f.lenMask & (uint64(1) << bit)) == 0 {
		if f.allASCII && (lo|hi)&0x80808080 == 0 && (n <= 8 || isPureASCII(name)) {
			return -1
		}
	}
	return f.getCaseInsensitive(name, n, lo, hi)
}

func (f *FieldCache) asciiFoldCandidate(name string, n uint32) bool {
	if n == 0 {
		return (f.lenMask & 1) != 0
	}
	bit := uint(n)
	if bit > 63 {
		bit = 63
	}
	if (f.lenMask & (uint64(1) << bit)) == 0 {
		return false
	}
	fb := foldASCIIByte(name[0])
	if fb < 64 {
		if (f.foldFirst0 & (uint64(1) << fb)) == 0 {
			return false
		}
	} else {
		if (f.foldFirst1 & (uint64(1) << (fb - 64))) == 0 {
			return false
		}
	}
	lb := foldASCIIByte(name[n-1])
	if lb < 64 {
		if (f.foldLast0 & (uint64(1) << lb)) == 0 {
			return false
		}
	} else {
		if (f.foldLast1 & (uint64(1) << (lb - 64))) == 0 {
			return false
		}
	}
	return true
}

func (f *FieldCache) getCaseInsensitive(name string, n, lo, hi uint32) int {
	if f.allASCII && (lo|hi)&0x80808080 == 0 && !f.asciiFoldCandidate(name, n) {
		if n <= 8 || isPureASCII(name) {
			return -1
		}
	}
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
		midEnd := int(n) - 4
		for {
			e := &f.folded[idx]
			if e.idxPlus1 == 0 {
				break
			}
			if e.n == n && e.lo == loFold && e.hi == hiFold {
				off := 4
				matched := true
				for off+8 <= midEnd {
					w := readU64LE(name, off)
					if w&0x8080808080808080 != 0 {
						goto unicode
					}
					ew := readU64LE(e.name, off)
					if foldASCII8(w) != ew {
						matched = false
						break
					}
					off += 8
				}
				if matched {
					for off < midEnd {
						b := name[off]
						if b >= 0x80 {
							goto unicode
						}
						if b >= 'A' && b <= 'Z' {
							b += 'a' - 'A'
						}
						if b != e.name[off] {
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
		off := 4
		for off+8 <= midEnd {
			if readU64LE(name, off)&0x8080808080808080 != 0 {
				goto unicode
			}
			off += 8
		}
		for off < midEnd {
			if name[off] >= 0x80 {
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
