//go:build arm64 && go1.20 && !go1.28

// Copyright 2021 ByteDance Inc.
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
// https://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package optdec

import (
	"math"
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

// cParityDecimal follows native/atof_native.h's decimal buffer, binary shifts,
// and rounding. It uses the caller's existing scratch capacity, as C does.
type cParityDecimal struct {
	digits     unsafe.Pointer
	capacity   int
	nd, dp     int32
	neg, trunc bool
}

func (d *cParityDecimal) byteAt(i int32) byte {
	return *(*byte)(unsafe.Add(d.digits, i))
}

func (d *cParityDecimal) put(i int32, c byte) {
	*(*byte)(unsafe.Add(d.digits, i)) = c
}

// The numeric scanner has already validated the token. For a token fitting
// the C decimal buffer, strconv supplies the same correctly rounded result
// and can retry its wider-significand fast path before decimal conversion.
// Keep the C implementation for larger tokens or undersized scratch: its
// bounded decimal-buffer behavior is part of the experimental C contract.
func cParityAtofFallback(src unsafe.Pointer, size int, buf *[]byte) float64 {
	if size <= 800 && cap(*buf) >= 800 {
		value, _ := strconv.ParseFloat(rt.Mem2Str(unsafe.Slice((*byte)(src), size)), 64)
		return value
	}
	return cParityAtofNative(src, size, buf)
}

func cParityAtofNative(src unsafe.Pointer, size int, buf *[]byte) float64 {
	d := cParityDecimal{digits: unsafe.Pointer(rt.SliceData(*buf)), capacity: cap(*buf)}
	// A slice clear lets the compiler use its bulk zeroing operation while
	// preserving the C scratch-buffer contract, including unused capacity.
	scratch := (*buf)[:cap(*buf)]
	for i := range scratch {
		scratch[i] = 0
	}
	d.set(src, size)
	return d.float64()
}

func (d *cParityDecimal) set(src unsafe.Pointer, size int) {
	i := 0
	if cParityNumberByte(src) == '-' {
		i++
		d.neg = true
	}
	sawDot := false
	for ; i < size; i++ {
		c := cParityNumberByte(unsafe.Add(src, i))
		if cParityNumberDigit(c) {
			if c == '0' && d.nd == 0 {
				d.dp--
				continue
			}
			if int(d.nd) < d.capacity {
				d.put(d.nd, c)
				d.nd++
			} else if c != '0' {
				d.trunc = true
			}
		} else if c == '.' {
			sawDot = true
			d.dp = d.nd
		} else {
			break
		}
	}
	if !sawDot {
		d.dp = d.nd
	}
	if i < size && (cParityNumberByte(unsafe.Add(src, i)) == 'e' || cParityNumberByte(unsafe.Add(src, i)) == 'E') {
		exp, sign := int32(0), int32(1)
		i++
		if cParityNumberByte(unsafe.Add(src, i)) == '+' {
			i++
		} else if cParityNumberByte(unsafe.Add(src, i)) == '-' {
			i++
			sign = -1
		}
		for ; i < size && cParityNumberDigit(cParityNumberByte(unsafe.Add(src, i))) && exp < 10000; i++ {
			exp = exp*10 + int32(cParityNumberByte(unsafe.Add(src, i))-'0')
		}
		d.dp += exp * sign
	}
}

func (d *cParityDecimal) trim() {
	for d.nd > 0 && d.byteAt(d.nd-1) == '0' {
		d.nd--
	}
	if d.nd == 0 {
		d.dp = 0
	}
}

func (d *cParityDecimal) rightShift(k uint32) {
	r, w := int32(0), int32(0)
	var n uint64
	for ; n>>k == 0; r++ {
		if r >= d.nd {
			if n == 0 {
				d.nd = 0
				return
			}
			for n>>k == 0 {
				n *= 10
				r++
			}
			break
		}
		n = n*10 + uint64(d.byteAt(r)) - '0'
	}
	d.dp -= r - 1
	mask := uint64(1)<<k - 1
	for ; r < d.nd; r++ {
		digit := n >> k
		n &= mask
		d.put(w, byte(digit+'0'))
		w++
		n = n*10 + uint64(d.byteAt(r)) - '0'
	}
	for n > 0 {
		digit := n >> k
		n &= mask
		if int(w) < d.capacity {
			d.put(w, byte(digit+'0'))
			w++
		} else if digit > 0 {
			d.trunc = true
		}
		n *= 10
	}
	d.nd = w
	d.trim()
}

func (d *cParityDecimal) prefixLess(cutoff string) bool {
	i := int32(0)
	for ; i < d.nd; i++ {
		if int(i) == len(cutoff) {
			return false
		}
		if d.byteAt(i) != cutoff[i] {
			return d.byteAt(i) < cutoff[i]
		}
	}
	return int(i) < len(cutoff)
}

func (d *cParityDecimal) leftShift(k uint32) {
	entry := cParityLeftShiftTable[k]
	delta := entry.delta
	if d.prefixLess(entry.cutoff) {
		delta--
	}
	r, w := d.nd, d.nd+delta
	var n uint64
	for r--; r >= 0; r-- {
		n += uint64(d.byteAt(r)-'0') << k
		quotient := n / 10
		remainder := n - 10*quotient
		w--
		if int(w) < d.capacity {
			d.put(w, byte(remainder+'0'))
		} else if remainder != 0 {
			d.trunc = true
		}
		n = quotient
	}
	for n > 0 {
		quotient := n / 10
		remainder := n - 10*quotient
		w--
		if int(w) < d.capacity {
			d.put(w, byte(remainder+'0'))
		} else if remainder != 0 {
			d.trunc = true
		}
		n = quotient
	}
	d.nd += delta
	if int(d.nd) >= d.capacity {
		d.nd = int32(d.capacity)
	}
	d.dp += delta
	d.trim()
}

func (d *cParityDecimal) shift(k int32) {
	if d.nd == 0 || k == 0 {
		return
	}
	if k > 0 {
		for k > 60 {
			d.leftShift(60)
			k -= 60
		}
		if k != 0 {
			d.leftShift(uint32(k))
		}
	} else {
		for k < -60 {
			d.rightShift(60)
			k += 60
		}
		if k != 0 {
			d.rightShift(uint32(-k))
		}
	}
}

func (d *cParityDecimal) shouldRoundUp(nd int32) bool {
	if nd < 0 || nd >= d.nd {
		return false
	}
	if d.byteAt(nd) == '5' && nd+1 == d.nd {
		if d.trunc {
			return true
		}
		return nd > 0 && (d.byteAt(nd-1)-'0')%2 != 0
	}
	return d.byteAt(nd) >= '5'
}

func (d *cParityDecimal) roundedInteger() uint64 {
	if d.dp > 20 {
		return ^uint64(0)
	}
	i := int32(0)
	var n uint64
	for ; i < d.dp && i < d.nd; i++ {
		n = n*10 + uint64(d.byteAt(i)-'0')
	}
	for ; i < d.dp; i++ {
		n *= 10
	}
	if d.shouldRoundUp(d.dp) {
		n++
	}
	return n
}

func (d *cParityDecimal) float64() float64 {
	exp2, n := int32(0), int32(0)
	var mant uint64
	if d.nd == 0 || d.dp < -330 {
		exp2 = -1023
		goto out
	}
	if d.dp > 310 {
		goto overflow
	}
	for d.dp > 0 {
		if d.dp >= 9 {
			n = 27
		} else {
			n = cParityDecimalPowTable[d.dp]
		}
		d.shift(-n)
		exp2 += n
	}
	for d.dp < 0 || d.dp == 0 && d.byteAt(0) < '5' {
		if -d.dp >= 9 {
			n = 27
		} else {
			n = cParityDecimalPowTable[-d.dp]
		}
		d.shift(n)
		exp2 -= n
	}
	exp2--
	if exp2 < -1022 {
		n = -1022 - exp2
		d.shift(-n)
		exp2 += n
	}
	if exp2+1023 >= 0x7ff {
		goto overflow
	}
	d.shift(53)
	mant = d.roundedInteger()
	if mant == 2<<52 {
		mant >>= 1
		exp2++
		if exp2+1023 >= 0x7ff {
			goto overflow
		}
	}
	if mant&(1<<52) == 0 {
		exp2 = -1023
	}
	goto out
overflow:
	mant, exp2 = 0, 0x7ff-1023
out:
	u := mant&0x000fffffffffffff | uint64((exp2+1023)&0x7ff)<<52
	if d.neg {
		u |= 1 << 63
	}
	return math.Float64frombits(u)
}
