//go:build (amd64 && go1.16 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

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

package alg

import (
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
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

func tryQuoteSmall(sp unsafe.Pointer, nb int, dp unsafe.Pointer) bool {
	if nb >= 8 {
		if nb <= 16 {
			w0 := *(*uint64)(sp)
			w1 := *(*uint64)(unsafe.Add(sp, nb-8))
			if ((quoteEscapeBits64(w0) | quoteEscapeBits64(w1)) & quoteMSB64) == 0 {
				*(*uint64)(dp) = w0
				*(*uint64)(unsafe.Add(dp, nb-8)) = w1
				return true
			}
			return false
		}
		w0 := *(*uint64)(sp)
		w1 := *(*uint64)(unsafe.Add(sp, 8))
		w2 := *(*uint64)(unsafe.Add(sp, nb-16))
		w3 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteEscapeBits64(w0) | quoteEscapeBits64(w1) | quoteEscapeBits64(w2) | quoteEscapeBits64(w3)) & quoteMSB64) == 0 {
			*(*uint64)(dp) = w0
			*(*uint64)(unsafe.Add(dp, 8)) = w1
			*(*uint64)(unsafe.Add(dp, nb-16)) = w2
			*(*uint64)(unsafe.Add(dp, nb-8)) = w3
			return true
		}
		return false
	}
	if nb >= 4 {
		u0 := *(*uint32)(sp)
		u1 := *(*uint32)(unsafe.Add(sp, nb-4))
		w := uint64(u0) | (uint64(u1) << 32)
		if (quoteEscapeBits64(w) & quoteMSB64) == 0 {
			*(*uint32)(dp) = u0
			*(*uint32)(unsafe.Add(dp, nb-4)) = u1
			return true
		}
		return false
	}
	b0 := *(*byte)(sp)
	b1 := *(*byte)(unsafe.Add(sp, nb>>1))
	b2 := *(*byte)(unsafe.Add(sp, nb-1))
	if b0 >= 0x20 && b0 != '"' && b0 != '\\' &&
		b1 >= 0x20 && b1 != '"' && b1 != '\\' &&
		b2 >= 0x20 && b2 != '"' && b2 != '\\' {
		*(*byte)(dp) = b0
		*(*byte)(unsafe.Add(dp, nb>>1)) = b1
		*(*byte)(unsafe.Add(dp, nb-1)) = b2
		return true
	}
	return false
}

// QuoteWithPaddedKey appends a struct key (32B-padded when len <= 32, or raw when len > 32) followed by '"' + escaped(val) + '"' to buf.
func QuoteWithPaddedKey(buf []byte, keyPtr *byte, nk int, val string) []byte {
	nb := len(val)
	rem := cap(buf) - len(buf)
	if (nk <= 32 && rem >= 34+nb) || (nk <= 64 && nk > 32 && rem >= nk+2+nb) {
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		dp := unsafe.Add(basePtr, bLen)
		if nk <= 32 {
			dk := (*[4]uint64)(dp)
			sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
			dk[0], dk[1] = sk[0], sk[1]
			if nk > 16 {
				dk[2], dk[3] = sk[2], sk[3]
			}
		} else {
			dk0 := (*[4]uint64)(dp)
			sk0 := (*[4]uint64)(unsafe.Pointer(keyPtr))
			dk0[0], dk0[1], dk0[2], dk0[3] = sk0[0], sk0[1], sk0[2], sk0[3]
			dk1 := (*[4]uint64)(unsafe.Add(dp, nk-32))
			sk1 := (*[4]uint64)(unsafe.Add(unsafe.Pointer(keyPtr), nk-32))
			dk1[0], dk1[1], dk1[2], dk1[3] = sk1[0], sk1[1], sk1[2], sk1[3]
		}
		dp = unsafe.Add(dp, nk)
		if nb == 0 {
			*(*[2]byte)(dp) = [2]byte{'"', '"'}
			return buf[:bLen+nk+2]
		}
		*(*byte)(dp) = '"'
		sp := unsafe.Pointer(unsafe.StringData(val))
		dp = unsafe.Add(dp, 1)
		if nb <= 32 {
			if nb >= 8 {
				if nb <= 16 {
					w0 := *(*uint64)(sp)
					w1 := *(*uint64)(unsafe.Add(sp, nb-8))
					if ((quoteEscapeBits64(w0) | quoteEscapeBits64(w1)) & quoteMSB64) == 0 {
						*(*uint64)(dp) = w0
						*(*uint64)(unsafe.Add(dp, nb-8)) = w1
						*(*byte)(unsafe.Add(dp, nb)) = '"'
						return buf[:bLen+nk+nb+2]
					}
				} else {
					w0 := *(*uint64)(sp)
					w1 := *(*uint64)(unsafe.Add(sp, 8))
					w2 := *(*uint64)(unsafe.Add(sp, nb-16))
					w3 := *(*uint64)(unsafe.Add(sp, nb-8))
					if ((quoteEscapeBits64(w0) | quoteEscapeBits64(w1) | quoteEscapeBits64(w2) | quoteEscapeBits64(w3)) & quoteMSB64) == 0 {
						*(*uint64)(dp) = w0
						*(*uint64)(unsafe.Add(dp, 8)) = w1
						*(*uint64)(unsafe.Add(dp, nb-16)) = w2
						*(*uint64)(unsafe.Add(dp, nb-8)) = w3
						*(*byte)(unsafe.Add(dp, nb)) = '"'
						return buf[:bLen+nk+nb+2]
					}
				}
			} else if nb >= 4 {
				u0 := *(*uint32)(sp)
				u1 := *(*uint32)(unsafe.Add(sp, nb-4))
				w := uint64(u0) | (uint64(u1) << 32)
				if (quoteEscapeBits64(w) & quoteMSB64) == 0 {
					*(*uint32)(dp) = u0
					*(*uint32)(unsafe.Add(dp, nb-4)) = u1
					*(*byte)(unsafe.Add(dp, nb)) = '"'
					return buf[:bLen+nk+nb+2]
				}
			} else {
				b0 := *(*byte)(sp)
				b1 := *(*byte)(unsafe.Add(sp, nb>>1))
				b2 := *(*byte)(unsafe.Add(sp, nb-1))
				if b0 >= 0x20 && b0 != '"' && b0 != '\\' &&
					b1 >= 0x20 && b1 != '"' && b1 != '\\' &&
					b2 >= 0x20 && b2 != '"' && b2 != '\\' {
					*(*byte)(dp) = b0
					*(*byte)(unsafe.Add(dp, nb>>1)) = b1
					*(*byte)(unsafe.Add(dp, nb-1)) = b2
					*(*byte)(unsafe.Add(dp, nb)) = '"'
					return buf[:bLen+nk+nb+2]
				}
			}
		}
		return quotePlainTail(buf[:bLen+nk+1], val, 0)
	}
	return quoteWithPaddedKeySlow(buf, keyPtr, nk, val)
}

// quoteWithPaddedKeySlow handles overflow capacity or keys > 64B out-of-line
// so the fast path avoids extra spills.
//
//go:noinline
func quoteWithPaddedKeySlow(buf []byte, keyPtr *byte, nk int, val string) []byte {
	if nk > 0 {
		buf = append(buf, unsafe.Slice(keyPtr, nk)...)
	}
	return Quote(buf, val, false)
}

func Quote(buf []byte, val string, double bool) []byte {
	nb := len(val)
	if !double {
		if cap(buf)-len(buf) < nb+2 {
			buf = rt.GuardSlice2(buf, nb+2)
		}
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		bCap := cap(buf)
		dp := unsafe.Add(basePtr, bLen)
		*(*byte)(dp) = '"'
		if nb == 0 {
			*(*byte)(unsafe.Add(dp, 1)) = '"'
			return buf[:bLen+2]
		}
		sp := unsafe.Pointer(unsafe.StringData(val))
		dp = unsafe.Add(dp, 1)
		if nb <= 32 && tryQuoteSmall(sp, nb, dp) {
			*(*byte)(unsafe.Add(dp, nb)) = '"'
			return buf[:bLen+nb+2]
		}
		bLen++
		ret, dn := kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
		bLen += dn
		buf = buf[:bLen]
		if ret >= 0 {
			if bLen < bCap {
				*(*byte)(unsafe.Add(basePtr, bLen)) = '"'
				return buf[:bLen+1]
			}
			return append(buf, '"')
		}
		for {
			buf = rt.GuardSlice2(buf, cap(buf))
			basePtr = unsafe.Pointer(unsafe.SliceData(buf))
			bLen = len(buf)
			bCap = cap(buf)
			ret = ^ret
			nb -= ret
			sp = unsafe.Add(sp, ret)
			dp = unsafe.Add(basePtr, bLen)
			ret, dn = kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
			bLen += dn
			buf = buf[:bLen]
			if ret >= 0 {
				break
			}
		}
		runtime.KeepAlive(val)
		return append(buf, '"')
	}

	if nb == 0 {
		return append(buf, `"\"\""`...)
	}
	buf = append(buf, `"\"`...)
	sp := unsafe.Pointer(unsafe.StringData(val))
	buf = rt.GuardSlice2(buf, nb+1)

	for nb > 0 {
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		bCap := cap(buf)
		dp := unsafe.Add(basePtr, bLen)
		ret, dn := kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, types.F_DOUBLE_UNQUOTE)
		buf = buf[:bLen+dn]
		if ret >= 0 {
			break
		}
		buf = rt.GuardSlice2(buf, cap(buf))
		ret = ^ret
		nb -= ret
		if nb > 0 {
			sp = unsafe.Add(sp, ret)
		}
	}

	runtime.KeepAlive(buf)
	runtime.KeepAlive(sp)
	return append(buf, `\""`...)
}

// QuoteKey appends '"' + escaped(val) + '":' to buf.
func QuoteKey(buf []byte, val string) []byte {
	nb := len(val)
	if cap(buf)-len(buf) < nb+3 {
		buf = rt.GuardSlice2(buf, nb+3)
	}
	basePtr := unsafe.Pointer(unsafe.SliceData(buf))
	bLen := len(buf)
	bCap := cap(buf)
	dp := unsafe.Add(basePtr, bLen)
	*(*byte)(dp) = '"'
	if nb == 0 {
		*(*byte)(unsafe.Add(dp, 1)) = '"'
		*(*byte)(unsafe.Add(dp, 2)) = ':'
		return buf[:bLen+3]
	}
	sp := unsafe.Pointer(unsafe.StringData(val))
	dp = unsafe.Add(dp, 1)
	if nb <= 32 && tryQuoteSmall(sp, nb, dp) {
		*(*byte)(unsafe.Add(dp, nb)) = '"'
		*(*byte)(unsafe.Add(dp, nb+1)) = ':'
		return buf[:bLen+nb+3]
	}
	bLen++
	ret, dn := kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
	bLen += dn
	buf = buf[:bLen]
	if ret >= 0 {
		if bLen+2 <= bCap {
			dp = unsafe.Add(basePtr, bLen)
			*(*byte)(dp) = '"'
			*(*byte)(unsafe.Add(dp, 1)) = ':'
			return buf[:bLen+2]
		}
		return append(buf, '"', ':')
	}
	for {
		buf = rt.GuardSlice2(buf, cap(buf))
		basePtr = unsafe.Pointer(unsafe.SliceData(buf))
		bLen = len(buf)
		bCap = cap(buf)
		ret = ^ret
		nb -= ret
		sp = unsafe.Add(sp, ret)
		dp = unsafe.Add(basePtr, bLen)
		ret, dn = kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
		bLen += dn
		buf = buf[:bLen]
		if ret >= 0 {
			break
		}
	}
	runtime.KeepAlive(val)
	return append(buf, '"', ':')
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

func tryQuoteHTMLNoValSmall(sp unsafe.Pointer, nb int, dp unsafe.Pointer) bool {
	if nb >= 8 {
		if nb <= 16 {
			w0 := *(*uint64)(sp)
			w1 := *(*uint64)(unsafe.Add(sp, nb-8))
			if ((quoteSafeHTMLNoValBits64(w0) | quoteSafeHTMLNoValBits64(w1)) & saniMSB64) == 0 {
				*(*uint64)(dp) = w0
				*(*uint64)(unsafe.Add(dp, nb-8)) = w1
				return true
			}
			return false
		}
		w0 := *(*uint64)(sp)
		w1 := *(*uint64)(unsafe.Add(sp, 8))
		w2 := *(*uint64)(unsafe.Add(sp, nb-16))
		w3 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLNoValBits64(w0) | quoteSafeHTMLNoValBits64(w1) | quoteSafeHTMLNoValBits64(w2) | quoteSafeHTMLNoValBits64(w3)) & saniMSB64) == 0 {
			*(*uint64)(dp) = w0
			*(*uint64)(unsafe.Add(dp, 8)) = w1
			*(*uint64)(unsafe.Add(dp, nb-16)) = w2
			*(*uint64)(unsafe.Add(dp, nb-8)) = w3
			return true
		}
		return false
	}
	if nb >= 4 {
		u0 := *(*uint32)(sp)
		u1 := *(*uint32)(unsafe.Add(sp, nb-4))
		w := uint64(u0) | (uint64(u1) << 32)
		if (quoteSafeHTMLNoValBits64(w) & saniMSB64) == 0 {
			*(*uint32)(dp) = u0
			*(*uint32)(unsafe.Add(dp, nb-4)) = u1
			return true
		}
		return false
	}
	b0 := *(*byte)(sp)
	b1 := *(*byte)(unsafe.Add(sp, nb>>1))
	b2 := *(*byte)(unsafe.Add(sp, nb-1))
	if isSafeHTMLNoValByte(b0) && isSafeHTMLNoValByte(b1) && isSafeHTMLNoValByte(b2) {
		*(*byte)(dp) = b0
		*(*byte)(unsafe.Add(dp, nb>>1)) = b1
		*(*byte)(unsafe.Add(dp, nb-1)) = b2
		return true
	}
	return false
}

func tryQuoteHTMLASCIISmall(sp unsafe.Pointer, nb int, dp unsafe.Pointer) bool {
	if nb >= 8 {
		if nb <= 16 {
			w0 := *(*uint64)(sp)
			w1 := *(*uint64)(unsafe.Add(sp, nb-8))
			if ((quoteSafeHTMLBits64(w0) | quoteSafeHTMLBits64(w1)) & saniMSB64) == 0 {
				*(*uint64)(dp) = w0
				*(*uint64)(unsafe.Add(dp, nb-8)) = w1
				return true
			}
			return false
		}
		w0 := *(*uint64)(sp)
		w1 := *(*uint64)(unsafe.Add(sp, 8))
		w2 := *(*uint64)(unsafe.Add(sp, nb-16))
		w3 := *(*uint64)(unsafe.Add(sp, nb-8))
		if ((quoteSafeHTMLBits64(w0) | quoteSafeHTMLBits64(w1) | quoteSafeHTMLBits64(w2) | quoteSafeHTMLBits64(w3)) & saniMSB64) == 0 {
			*(*uint64)(dp) = w0
			*(*uint64)(unsafe.Add(dp, 8)) = w1
			*(*uint64)(unsafe.Add(dp, nb-16)) = w2
			*(*uint64)(unsafe.Add(dp, nb-8)) = w3
			return true
		}
		return false
	}
	if nb >= 4 {
		u0 := *(*uint32)(sp)
		u1 := *(*uint32)(unsafe.Add(sp, nb-4))
		w := uint64(u0) | (uint64(u1) << 32)
		if (quoteSafeHTMLBits64(w) & saniMSB64) == 0 {
			*(*uint32)(dp) = u0
			*(*uint32)(unsafe.Add(dp, nb-4)) = u1
			return true
		}
		return false
	}
	b0 := *(*byte)(sp)
	b1 := *(*byte)(unsafe.Add(sp, nb>>1))
	b2 := *(*byte)(unsafe.Add(sp, nb-1))
	if isSafeHTMLASCIIByte(b0) && isSafeHTMLASCIIByte(b1) && isSafeHTMLASCIIByte(b2) {
		*(*byte)(dp) = b0
		*(*byte)(unsafe.Add(dp, nb>>1)) = b1
		*(*byte)(unsafe.Add(dp, nb-1)) = b2
		return true
	}
	return false
}

func quoteHTMLGrowLoop(buf []byte, sp unsafe.Pointer, nb int, val string, ret int, suffix byte) []byte {
	for {
		buf = rt.GuardSlice2(buf, cap(buf)+16)
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		bCap := cap(buf)
		ret = ^ret
		nb -= ret
		sp = unsafe.Add(sp, ret)
		dp := unsafe.Add(basePtr, bLen)
		var dn int
		ret, dn = kernels.QuoteHTMLBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen)
		bLen += dn
		buf = buf[:bLen]
		if ret >= 0 {
			break
		}
	}
	runtime.KeepAlive(val)
	if suffix != 0 {
		return append(buf, '"', suffix)
	}
	return append(buf, '"')
}

func quoteHTMLTail(buf []byte, val string, suffix byte) []byte {
	nb := len(val)
	basePtr := unsafe.Pointer(unsafe.SliceData(buf))
	bLen := len(buf)
	bCap := cap(buf)
	sp := unsafe.Pointer(unsafe.StringData(val))
	dp := unsafe.Add(basePtr, bLen)
	ret, dn := kernels.QuoteHTMLBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen)
	runtime.KeepAlive(val)
	bLen += dn
	buf = buf[:bLen]
	if ret >= 0 {
		if suffix != 0 {
			if bLen+2 <= bCap {
				ep := unsafe.Add(basePtr, bLen)
				*(*byte)(ep) = '"'
				*(*byte)(unsafe.Add(ep, 1)) = suffix
				return buf[:bLen+2]
			}
			return append(buf, '"', suffix)
		}
		if bLen < bCap {
			*(*byte)(unsafe.Add(basePtr, bLen)) = '"'
			return buf[:bLen+1]
		}
		return append(buf, '"')
	}
	return quoteHTMLGrowLoop(buf, sp, nb, val, ret, suffix)
}

func quotePlainTail(buf []byte, val string, suffix byte) []byte {
	nb := len(val)
	basePtr := unsafe.Pointer(unsafe.SliceData(buf))
	bLen := len(buf)
	bCap := cap(buf)
	sp := unsafe.Pointer(unsafe.StringData(val))
	dp := unsafe.Add(basePtr, bLen)
	ret, dn := kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
	runtime.KeepAlive(val)
	bLen += dn
	buf = buf[:bLen]
	if ret >= 0 {
		if suffix != 0 {
			if bLen+2 <= bCap {
				ep := unsafe.Add(basePtr, bLen)
				*(*byte)(ep) = '"'
				*(*byte)(unsafe.Add(ep, 1)) = suffix
				return buf[:bLen+2]
			}
			return append(buf, '"', suffix)
		}
		if bLen < bCap {
			*(*byte)(unsafe.Add(basePtr, bLen)) = '"'
			return buf[:bLen+1]
		}
		return append(buf, '"')
	}
	for {
		buf = rt.GuardSlice2(buf, cap(buf)+16)
		basePtr = unsafe.Pointer(unsafe.SliceData(buf))
		bLen = len(buf)
		bCap = cap(buf)
		ret = ^ret
		nb -= ret
		sp = unsafe.Add(sp, ret)
		dp = unsafe.Add(basePtr, bLen)
		ret, dn = kernels.QuoteBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen, 0)
		bLen += dn
		buf = buf[:bLen]
		if ret >= 0 {
			break
		}
	}
	runtime.KeepAlive(val)
	if suffix != 0 {
		return append(buf, '"', suffix)
	}
	return append(buf, '"')
}

func QuoteWithPaddedKeyFlags(buf []byte, keyPtr *byte, keyLen int, val string, sanitize uint64) []byte {
	if sanitize == 0 {
		return QuoteWithPaddedKey(buf, keyPtr, keyLen, val)
	}
	nb := len(val)
	rem := cap(buf) - len(buf)
	if (keyLen <= 32 && rem >= 34+nb) || (keyLen <= 64 && keyLen > 32 && rem >= keyLen+2+nb) {
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		bCap := cap(buf)
		dp := unsafe.Add(basePtr, bLen)
		if keyLen <= 32 {
			sk := (*[4]uint64)(unsafe.Pointer(keyPtr))
			dk := (*[4]uint64)(dp)
			dk[0], dk[1] = sk[0], sk[1]
			if keyLen > 16 {
				dk[2], dk[3] = sk[2], sk[3]
			}
		} else {
			*(*[4]uint64)(dp) = *(*[4]uint64)(unsafe.Pointer(keyPtr))
			*(*[4]uint64)(unsafe.Add(dp, keyLen-32)) = *(*[4]uint64)(unsafe.Add(unsafe.Pointer(keyPtr), keyLen-32))
		}
		dp = unsafe.Add(dp, keyLen)
		if nb == 0 {
			*(*[2]byte)(dp) = [2]byte{'"', '"'}
			return buf[:bLen+keyLen+2]
		}
		*(*byte)(dp) = '"'
		sp := unsafe.Pointer(unsafe.StringData(val))
		dp = unsafe.Add(dp, 1)
		if sanitize == (1 << BitEscapeHTML) {
			if nb <= 32 && tryQuoteHTMLNoValSmall(sp, nb, dp) {
				*(*byte)(unsafe.Add(dp, nb)) = '"'
				return buf[:bLen+keyLen+nb+2]
			}
			bLen += keyLen + 1
			ret, dn := kernels.QuoteHTMLBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen)
			runtime.KeepAlive(val)
			bLen += dn
			buf = buf[:bLen]
			if ret >= 0 {
				if bLen < bCap {
					*(*byte)(unsafe.Add(basePtr, bLen)) = '"'
					return buf[:bLen+1]
				}
				return append(buf, '"')
			}
			return quoteHTMLGrowLoop(buf, sp, nb, val, ret, 0)
		}
		if nb <= 32 && tryQuoteHTMLASCIISmall(sp, nb, dp) {
			*(*byte)(unsafe.Add(dp, nb)) = '"'
			return buf[:bLen+keyLen+nb+2]
		}
		buf = buf[:bLen+keyLen+1]
		if utf8.ValidateString(val) {
			if sanitize&(1<<BitEscapeHTML) != 0 {
				return quoteHTMLTail(buf, val, 0)
			}
			return quotePlainTail(buf, val, 0)
		}
		return appendQuoteSanitizedTail(buf, val, sanitize, 0)
	}
	if keyLen > 0 {
		buf = append(buf, unsafe.Slice(keyPtr, keyLen)...)
	}
	return QuoteFlags(buf, val, false, sanitize)
}

func QuoteFlags(buf []byte, val string, double bool, sanitize uint64) []byte {
	if sanitize == 0 {
		return Quote(buf, val, double)
	}
	if !double {
		nb := len(val)
		if cap(buf)-len(buf) < nb+2 {
			buf = rt.GuardSlice2(buf, nb+2)
		}
		basePtr := unsafe.Pointer(unsafe.SliceData(buf))
		bLen := len(buf)
		bCap := cap(buf)
		dp := unsafe.Add(basePtr, bLen)
		*(*byte)(dp) = '"'
		if nb == 0 {
			*(*byte)(unsafe.Add(dp, 1)) = '"'
			return buf[:bLen+2]
		}
		sp := unsafe.Pointer(unsafe.StringData(val))
		dp = unsafe.Add(dp, 1)
		if sanitize == (1 << BitEscapeHTML) {
			if nb <= 32 && tryQuoteHTMLNoValSmall(sp, nb, dp) {
				*(*byte)(unsafe.Add(dp, nb)) = '"'
				return buf[:bLen+nb+2]
			}
			bLen++
			ret, dn := kernels.QuoteHTMLBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen)
			runtime.KeepAlive(val)
			bLen += dn
			buf = buf[:bLen]
			if ret >= 0 {
				if bLen < bCap {
					*(*byte)(unsafe.Add(basePtr, bLen)) = '"'
					return buf[:bLen+1]
				}
				return append(buf, '"')
			}
			return quoteHTMLGrowLoop(buf, sp, nb, val, ret, 0)
		}
		if nb <= 32 && tryQuoteHTMLASCIISmall(sp, nb, dp) {
			*(*byte)(unsafe.Add(dp, nb)) = '"'
			return buf[:bLen+nb+2]
		}
		buf = buf[:bLen+1]
		if utf8.ValidateString(val) {
			if sanitize&(1<<BitEscapeHTML) != 0 {
				return quoteHTMLTail(buf, val, 0)
			}
			return quotePlainTail(buf, val, 0)
		}
		return appendQuoteSanitizedTail(buf, val, sanitize, 0)
	}
	mark := len(buf)
	buf = Quote(buf, val, true)
	return SanitizeAppended(buf, mark, sanitize)
}

func QuoteKeyFlags(buf []byte, val string, sanitize uint64) []byte {
	if sanitize == 0 {
		return QuoteKey(buf, val)
	}
	nb := len(val)
	if cap(buf)-len(buf) < nb+3 {
		buf = rt.GuardSlice2(buf, nb+3)
	}
	basePtr := unsafe.Pointer(unsafe.SliceData(buf))
	bLen := len(buf)
	bCap := cap(buf)
	dp := unsafe.Add(basePtr, bLen)
	*(*byte)(dp) = '"'
	if nb == 0 {
		*(*byte)(unsafe.Add(dp, 1)) = '"'
		*(*byte)(unsafe.Add(dp, 2)) = ':'
		return buf[:bLen+3]
	}
	sp := unsafe.Pointer(unsafe.StringData(val))
	dp = unsafe.Add(dp, 1)
	if sanitize == (1 << BitEscapeHTML) {
		if nb <= 32 && tryQuoteHTMLNoValSmall(sp, nb, dp) {
			*(*byte)(unsafe.Add(dp, nb)) = '"'
			*(*byte)(unsafe.Add(dp, nb+1)) = ':'
			return buf[:bLen+nb+3]
		}
		bLen++
		ret, dn := kernels.QuoteHTMLBuffer((*byte)(sp), nb, (*byte)(dp), bCap-bLen)
		runtime.KeepAlive(val)
		bLen += dn
		buf = buf[:bLen]
		if ret >= 0 {
			if bLen+2 <= bCap {
				ep := unsafe.Add(basePtr, bLen)
				*(*byte)(ep) = '"'
				*(*byte)(unsafe.Add(ep, 1)) = ':'
				return buf[:bLen+2]
			}
			return append(buf, '"', ':')
		}
		return quoteHTMLGrowLoop(buf, sp, nb, val, ret, ':')
	}
	if nb <= 32 && tryQuoteHTMLASCIISmall(sp, nb, dp) {
		*(*byte)(unsafe.Add(dp, nb)) = '"'
		*(*byte)(unsafe.Add(dp, nb+1)) = ':'
		return buf[:bLen+nb+3]
	}
	buf = buf[:bLen+1]
	if utf8.ValidateString(val) {
		if sanitize&(1<<BitEscapeHTML) != 0 {
			return quoteHTMLTail(buf, val, ':')
		}
		return quotePlainTail(buf, val, ':')
	}
	return appendQuoteSanitizedTail(buf, val, sanitize, ':')
}
