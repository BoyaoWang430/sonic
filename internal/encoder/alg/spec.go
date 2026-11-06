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
	m := types.NewStateMachine()
	ret := native.ValidateOne(&s, &p, m, 0)
	types.FreeStateMachine(m)

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

var typeByte = rt.UnpackEface(byte(0)).Type

func Quote(buf []byte, val string, double bool) []byte {
	nb := len(val)
	if !double {
		if nb == 0 {
			return append(buf, '"', '"')
		}
		if cap(buf)-len(buf) < nb+2 {
			buf = rt.GuardSlice2(buf, nb+2)
		}
		b := (*rt.GoSlice)(unsafe.Pointer(&buf))
		*(*byte)(unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))) = '"'
		b.Len++
		sp := rt.IndexChar(val, 0)
		dp := unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))
		dn := b.Cap - b.Len
		ret := native.Quote(sp, nb, dp, &dn, 0)
		b.Len += dn
		if ret >= 0 {
			if b.Len < b.Cap {
				*(*byte)(unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))) = '"'
				b.Len++
				return buf
			}
			return append(buf, '"')
		}
		for {
			*b = rt.GrowSlice(typeByte, *b, b.Cap*2)
			ret = ^ret
			nb -= ret
			sp = unsafe.Pointer(uintptr(sp) + uintptr(ret))
			dp = unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))
			dn = b.Cap - b.Len
			ret = native.Quote(sp, nb, dp, &dn, 0)
			b.Len += dn
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
	sp := rt.IndexChar(val, 0)
	buf = rt.GuardSlice2(buf, nb+1)
	b := (*rt.GoSlice)(unsafe.Pointer(&buf))

	for nb > 0 {
		dp := unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))
		dn := b.Cap - b.Len
		ret := native.Quote(sp, nb, dp, &dn, types.F_DOUBLE_UNQUOTE)
		b.Len += dn
		if ret >= 0 {
			break
		}
		*b = rt.GrowSlice(typeByte, *b, b.Cap*2)
		ret = ^ret
		nb -= ret
		if nb > 0 {
			sp = unsafe.Pointer(uintptr(sp) + uintptr(ret))
		}
	}

	runtime.KeepAlive(buf)
	runtime.KeepAlive(sp)
	return append(buf, `\""`...)
}

// QuoteKey appends '"' + escaped(val) + '":' to buf.
func QuoteKey(buf []byte, val string) []byte {
	nb := len(val)
	if nb == 0 {
		return append(buf, '"', '"', ':')
	}
	if cap(buf)-len(buf) < nb+3 {
		buf = rt.GuardSlice2(buf, nb+3)
	}
	b := (*rt.GoSlice)(unsafe.Pointer(&buf))
	*(*byte)(unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))) = '"'
	b.Len++
	sp := rt.IndexChar(val, 0)
	dp := unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))
	dn := b.Cap - b.Len
	ret := native.Quote(sp, nb, dp, &dn, 0)
	b.Len += dn
	if ret >= 0 {
		if b.Len+2 <= b.Cap {
			p := uintptr(b.Ptr) + uintptr(b.Len)
			*(*byte)(unsafe.Pointer(p)) = '"'
			*(*byte)(unsafe.Pointer(p + 1)) = ':'
			b.Len += 2
			return buf
		}
		return append(buf, '"', ':')
	}
	for {
		*b = rt.GrowSlice(typeByte, *b, b.Cap*2)
		ret = ^ret
		nb -= ret
		sp = unsafe.Pointer(uintptr(sp) + uintptr(ret))
		dp = unsafe.Pointer(uintptr(b.Ptr) + uintptr(b.Len))
		dn = b.Cap - b.Len
		ret = native.Quote(sp, nb, dp, &dn, 0)
		b.Len += dn
		if ret >= 0 {
			break
		}
	}
	runtime.KeepAlive(val)
	return append(buf, '"', ':')
}

func HtmlEscape(dst []byte, src []byte) []byte {
	var sidx int

	dst = append(dst, src[:0]...) // avoid check nil dst
	sbuf := (*rt.GoSlice)(unsafe.Pointer(&src))
	dbuf := (*rt.GoSlice)(unsafe.Pointer(&dst))

	/* grow dst if it is shorter */
	if cap(dst)-len(dst) < len(src)+types.BufPaddingSize {
		cap := len(src)*3/2 + types.BufPaddingSize
		*dbuf = rt.GrowSlice(typeByte, *dbuf, cap)
	}

	for sidx < sbuf.Len {
		sp := rt.Add(sbuf.Ptr, uintptr(sidx))
		dp := rt.Add(dbuf.Ptr, uintptr(dbuf.Len))

		sn := sbuf.Len - sidx
		dn := dbuf.Cap - dbuf.Len
		nb := native.HTMLEscape(sp, sn, dp, &dn)

		/* check for errors */
		if dbuf.Len += dn; nb >= 0 {
			break
		}

		/* not enough space, grow the slice and try again */
		sidx += ^nb
		*dbuf = rt.GrowSlice(typeByte, *dbuf, dbuf.Cap*2)
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
