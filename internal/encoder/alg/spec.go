//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/**
 * Copyright 2024 ByteDance Inc.
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

//go:generate go run gen_quote_spec.go

package alg

import (
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// Valid validates json and returns first non-blank character position,
// if it is only one valid json value.
// Otherwise returns invalid character position using start.
//
// Note: it does not check for the invalid UTF-8 characters.
func Valid(data []byte) (ok bool, start int) {
	n := len(data)
	if n == 0 {
		return false, -1
	}
	s := rt.Mem2Str(data)
	p := 0
	ret := native.ValidateOne(&s, &p, nil, 0)

	if ret < 0 {
		return false, p - 1
	}

	/* check for trailing spaces */
	for ; p < n; p++ {
		if (types.SPACE_MASK & (1 << data[p])) == 0 {
			return false, p
		}
	}

	return true, ret
}

const (
	quoteLSB64  = 0x0101010101010101
	quoteMSB64  = 0x8080808080808080
	quoteSp64   = 0x2020202020202020
	quoteQuot64 = 0x2222222222222222
	quoteBsl64  = 0x5c5c5c5c5c5c5c5c
)

func quoteEscapeBits64(w uint64) uint64 {
	q := w ^ quoteQuot64
	b := w ^ quoteBsl64
	return ((w - quoteSp64) & ^w) | ((q - quoteLSB64) & ^q) | ((b - quoteLSB64) & ^b)
}

func HtmlEscape(dst []byte, src []byte) []byte {
	var sidx int
	n := len(src)

	dst = append(dst, src[:0]...) // avoid check nil dst
	if n == 0 {
		return dst
	}

	/* grow dst if it is shorter */
	if cap(dst)-len(dst) < n {
		need := n*3/2 + types.BufPaddingSize
		dst = rt.GuardSlice2(dst, need)
	}
	sp0 := unsafe.Pointer(unsafe.SliceData(src))

	for sidx < n {
		sp := unsafe.Add(sp0, sidx)
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(dst)), len(dst))

		sn := n - sidx
		dn := cap(dst) - len(dst)
		nb := native.HTMLEscape(sp, sn, dp, &dn)

		/* check for errors */
		dst = dst[:len(dst)+dn]
		if nb >= 0 {
			break
		}

		/* not enough space, grow the slice and try again */
		sidx += ^nb
		dst = rt.GuardSlice2(dst, cap(dst))
	}
	return dst
}

const digits100 = "00010203040506070809" +
	"10111213141516171819" +
	"20212223242526272829" +
	"30313233343536373839" +
	"40414243444546474849" +
	"50515253545556575859" +
	"60616263646566676869" +
	"70717273747576777879" +
	"80818283848586878889" +
	"90919293949596979899"

var pow10U64 = [20]uint64{
	1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9,
	1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19,
}

func numDigitsU64(u uint64) int {
	nd := (bits.Len64(u) * 78913) >> 18
	if u >= pow10U64[nd] {
		nd++
	}
	return nd
}

func formatDigitsU64(a []byte, u uint64) {
	nd := len(a)
	for nd > 8 {
		x3210 := uint32(u % 1e8)
		u /= 1e8
		x32, x10 := x3210/1e4, x3210%1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		x3, x2 := (x32/100)*2, (x32%100)*2
		a[nd-1], a[nd-2] = digits100[x0+1], digits100[x0]
		a[nd-3], a[nd-4] = digits100[x1+1], digits100[x1]
		a[nd-5], a[nd-6] = digits100[x2+1], digits100[x2]
		a[nd-7], a[nd-8] = digits100[x3+1], digits100[x3]
		nd -= 8
	}
	x := uint32(u)
	if nd == 8 {
		x32, x10 := x/1e4, x%1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		x3, x2 := (x32/100)*2, (x32%100)*2
		a[7], a[6] = digits100[x0+1], digits100[x0]
		a[5], a[4] = digits100[x1+1], digits100[x1]
		a[3], a[2] = digits100[x2+1], digits100[x2]
		a[1], a[0] = digits100[x3+1], digits100[x3]
		return
	}
	if nd >= 4 {
		x10 := x % 1e4
		x /= 1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		a[nd-1], a[nd-2] = digits100[x0+1], digits100[x0]
		a[nd-3], a[nd-4] = digits100[x1+1], digits100[x1]
		nd -= 4
	}
	if nd >= 2 {
		x0 := (x % 1e2) * 2
		x /= 1e2
		a[nd-1], a[nd-2] = digits100[x0+1], digits100[x0]
		nd -= 2
	}
	if nd > 0 {
		a[0] = byte('0' + x)
	}
}

func u64toaLarge(buf []byte, u uint64) []byte {
	n := len(buf)
	if cap(buf)-n < 20 {
		buf = rt.GuardSlice2(buf, 20)
	}
	if u < 10000 {
		x := uint32(u)
		hi, lo := (x/100)*2, (x%100)*2
		if x < 1000 {
			buf = buf[:n+3]
			buf[n] = digits100[hi+1]
			buf[n+1] = digits100[lo]
			buf[n+2] = digits100[lo+1]
			return buf
		}
		buf = buf[:n+4]
		buf[n] = digits100[hi]
		buf[n+1] = digits100[hi+1]
		buf[n+2] = digits100[lo]
		buf[n+3] = digits100[lo+1]
		return buf
	}
	nd := numDigitsU64(u)
	buf = buf[:n+nd]
	formatDigitsU64(buf[n:n+nd], u)
	return buf
}

func i64toaNeg(buf []byte, u uint64) []byte {
	if u < 10 {
		return append(buf, '-', byte('0'+u))
	}
	if u < 100 {
		i := u * 2
		return append(buf, '-', digits100[i], digits100[i+1])
	}
	n := len(buf)
	if cap(buf)-n < 20 {
		buf = rt.GuardSlice2(buf, 20)
	}
	if u < 10000 {
		x := uint32(u)
		hi, lo := (x/100)*2, (x%100)*2
		if x < 1000 {
			buf = buf[:n+4]
			buf[n] = '-'
			buf[n+1] = digits100[hi+1]
			buf[n+2] = digits100[lo]
			buf[n+3] = digits100[lo+1]
			return buf
		}
		buf = buf[:n+5]
		buf[n] = '-'
		buf[n+1] = digits100[hi]
		buf[n+2] = digits100[hi+1]
		buf[n+3] = digits100[lo]
		buf[n+4] = digits100[lo+1]
		return buf
	}
	nd := numDigitsU64(u)
	buf = buf[:n+1+nd]
	buf[n] = '-'
	formatDigitsU64(buf[n+1:n+1+nd], u)
	return buf
}

func I64toa(buf []byte, v int64) []byte {
	if v >= 0 {
		if uint64(v) < 10 {
			return append(buf, byte('0'+v))
		}
		if uint64(v) < 100 {
			i := uint64(v) * 2
			return append(buf, digits100[i], digits100[i+1])
		}
		return u64toaLarge(buf, uint64(v))
	}
	return i64toaNeg(buf, uint64(-v))
}

func appendPaddedKeyI64Slow(buf []byte, keyPtr *byte, nk int, v int64) []byte {
	if nk > 0 {
		buf = append(buf, unsafe.Slice(keyPtr, nk)...)
	}
	return I64toa(buf, v)
}

func appendPaddedKeyU64Slow(buf []byte, keyPtr *byte, nk int, u uint64) []byte {
	if nk > 0 {
		buf = append(buf, unsafe.Slice(keyPtr, nk)...)
	}
	return U64toa(buf, u)
}

// AppendPaddedKeyI64 appends a 32-byte-padded struct key (len <= 32) followed by decimal v to buf.
func AppendPaddedKeyI64(buf []byte, keyPtr *byte, nk int, v int64) []byte {
	if cap(buf)-len(buf) >= 52 && uint(nk) <= 32 {
		bLen := len(buf)
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), bLen)
		dk := (*[4]uint64)(dp)
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk[0], dk[1] = sk[0], sk[1]
		if nk > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		pos := bLen + nk
		vp := unsafe.Add(dp, nk)
		if v >= 0 {
			u := uint64(v)
			if u < 10 {
				*(*byte)(vp) = byte('0' + u)
				return buf[:pos+1]
			}
			if u < 100 {
				i := u * 2
				*(*byte)(vp) = digits100[i]
				*(*byte)(unsafe.Add(vp, 1)) = digits100[i+1]
				return buf[:pos+2]
			}
			if u < 10000 {
				x := uint32(u)
				hi, lo := (x/100)*2, (x%100)*2
				if x < 1000 {
					*(*byte)(vp) = digits100[hi+1]
					*(*byte)(unsafe.Add(vp, 1)) = digits100[lo]
					*(*byte)(unsafe.Add(vp, 2)) = digits100[lo+1]
					return buf[:pos+3]
				}
				*(*byte)(vp) = digits100[hi]
				*(*byte)(unsafe.Add(vp, 1)) = digits100[hi+1]
				*(*byte)(unsafe.Add(vp, 2)) = digits100[lo]
				*(*byte)(unsafe.Add(vp, 3)) = digits100[lo+1]
				return buf[:pos+4]
			}
			nd := numDigitsU64(u)
			out := buf[:pos+nd]
			formatDigitsU64(out[pos:pos+nd], u)
			return out
		}
		u := uint64(-v)
		*(*byte)(vp) = '-'
		vp1 := unsafe.Add(vp, 1)
		if u < 10 {
			*(*byte)(vp1) = byte('0' + u)
			return buf[:pos+2]
		}
		if u < 100 {
			i := u * 2
			*(*byte)(vp1) = digits100[i]
			*(*byte)(unsafe.Add(vp1, 1)) = digits100[i+1]
			return buf[:pos+3]
		}
		nd := numDigitsU64(u)
		out := buf[:pos+1+nd]
		formatDigitsU64(out[pos+1:pos+1+nd], u)
		return out
	}
	return appendPaddedKeyI64Slow(buf, keyPtr, nk, v)
}

// AppendPaddedKeyU64 appends a 32-byte-padded struct key (len <= 32) followed by decimal u to buf.
func AppendPaddedKeyU64(buf []byte, keyPtr *byte, nk int, u uint64) []byte {
	if cap(buf)-len(buf) >= 52 && uint(nk) <= 32 {
		bLen := len(buf)
		dp := unsafe.Add(unsafe.Pointer(unsafe.SliceData(buf)), bLen)
		dk := (*[4]uint64)(dp)
		sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
		dk[0], dk[1] = sk[0], sk[1]
		if nk > 16 {
			dk[2], dk[3] = sk[2], sk[3]
		}
		pos := bLen + nk
		vp := unsafe.Add(dp, nk)
		if u < 10 {
			*(*byte)(vp) = byte('0' + u)
			return buf[:pos+1]
		}
		if u < 100 {
			i := u * 2
			*(*byte)(vp) = digits100[i]
			*(*byte)(unsafe.Add(vp, 1)) = digits100[i+1]
			return buf[:pos+2]
		}
		if u < 10000 {
			x := uint32(u)
			hi, lo := (x/100)*2, (x%100)*2
			if x < 1000 {
				*(*byte)(vp) = digits100[hi+1]
				*(*byte)(unsafe.Add(vp, 1)) = digits100[lo]
				*(*byte)(unsafe.Add(vp, 2)) = digits100[lo+1]
				return buf[:pos+3]
			}
			*(*byte)(vp) = digits100[hi]
			*(*byte)(unsafe.Add(vp, 1)) = digits100[hi+1]
			*(*byte)(unsafe.Add(vp, 2)) = digits100[lo]
			*(*byte)(unsafe.Add(vp, 3)) = digits100[lo+1]
			return buf[:pos+4]
		}
		nd := numDigitsU64(u)
		out := buf[:pos+nd]
		formatDigitsU64(out[pos:pos+nd], u)
		return out
	}
	return appendPaddedKeyU64Slow(buf, keyPtr, nk, u)
}

func U64toa(buf []byte, v uint64) []byte {
	if v < 10 {
		return append(buf, byte('0'+v))
	}
	if v < 100 {
		i := v * 2
		return append(buf, digits100[i], digits100[i+1])
	}
	return u64toaLarge(buf, v)
}
