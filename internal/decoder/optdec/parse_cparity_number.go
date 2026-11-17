//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/cpu"
)

// These routines implement Sonic's padded-input number parser contract.
// The caller owns a readable 64-byte sentinel after the logical document.
// Errors return a zero cParityNum and publish the exact error cursor.
type cParityNum struct {
	typ uint64
	val uint64
}

func cParityNumberByte(p unsafe.Pointer) byte { return *(*byte)(p) }
func cParityNumberDigit(c byte) bool          { return c >= '0' && c <= '9' }

func cParityParseNumber(cur *unsafe.Pointer, num *cParityNum, neg bool, dbuf *[]byte) ErrorCode {
	end, value, code := cParityParseNumberValue(*cur, neg, *dbuf)
	*cur = end
	if code == SONIC_OK {
		*num = value
	}
	return code
}

// Keep input, result and scratch ownership local to one numeric operation.
// Returning the cursor by value avoids publishing an interior pointer through
// a pointer argument on every completion.
func cParityParseNumberValue(p unsafe.Pointer, neg bool, dbuf []byte) (unsafe.Pointer, cParityNum, ErrorCode) {
	start := p
	var num cParityNum
	var sig uint64
	var exp, expDec int64
	expNeg := int64(1)
	trunc := false
	var value float64
	var converted bool
	var fracCount, needed, count int
	var code ErrorCode
	if cParityNumberByte(p) == '0' {
		p = unsafe.Add(p, 1)
		if cParityNumberByte(p) != '.' && cParityNumberByte(p) != 'e' && cParityNumberByte(p) != 'E' {
			num.typ, num.val = KUint, 0
			if neg {
				num.typ = KSint
			}
			goto done
		}
		if cParityNumberByte(p) == '.' {
			p = unsafe.Add(p, 1)
			dot := p
			if !cParityNumberDigit(cParityNumberByte(p)) {
				goto invalid
			}
			for cParityNumberByte(p) == '0' {
				p = unsafe.Add(p, 1)
			}
			if cParityNumberByte(p) == 'e' || cParityNumberByte(p) == 'E' {
				goto exponent
			}
			exp -= int64(uintptr(p) - uintptr(dot))
			goto fraction
		}
		goto exponent
	}
	// Accumulate the representable prefix once. Later digits contribute to
	// its decimal exponent; the twenty-digit integer case below still checks
	// its last digit exactly.
	for count < 19 && cParityNumberDigit(cParityNumberByte(p)) {
		sig = sig*10 + uint64(cParityNumberByte(p)-'0')
		p = unsafe.Add(p, 1)
		count++
	}
	if count == 0 {
		goto invalid
	}
	if cParityNumberDigit(cParityNumberByte(p)) {
		var skipped int64
		p, skipped = cParitySkipDigits(p)
		trunc = true
		exp += skipped
	}
	if cParityNumberByte(p) == '.' {
		p = unsafe.Add(p, 1)
		if !cParityNumberDigit(cParityNumberByte(p)) {
			goto invalid
		}
		goto fraction
	}
	if cParityNumberByte(p) != 'e' && cParityNumberByte(p) != 'E' {
		if exp == 0 {
			if neg {
				if sig > 1<<63 {
					num.typ, num.val = KReal, math.Float64bits(-float64(sig))
				} else {
					num.typ, num.val = KSint, -sig
				}
			} else {
				num.typ, num.val = KUint, sig
			}
			goto done
		}
		if exp == 1 {
			hi, lo := bits.Mul64(sig, 10)
			if hi != 0 {
				goto floating
			}
			res, carry := bits.Add64(lo, uint64(cParityNumberByte(unsafe.Add(p, -1))-'0'), 0)
			if carry != 0 {
				goto floating
			}
			if neg {
				num.typ, num.val = KReal, math.Float64bits(-float64(res))
			} else {
				num.typ, num.val = KUint, res
			}
			goto done
		}
		goto floating
	}
	goto exponent

fraction:
	fracCount, needed = 0, 17-count
	for needed > 0 && cParityNumberDigit(cParityNumberByte(p)) {
		sig = sig*10 + uint64(cParityNumberByte(p)-'0')
		p = unsafe.Add(p, 1)
		needed--
		fracCount++
	}
	exp -= int64(fracCount)
	if cParityNumberDigit(cParityNumberByte(p)) {
		p, _ = cParitySkipDigits(p)
		trunc = true
	}
	if cParityNumberByte(p) != 'e' && cParityNumberByte(p) != 'E' {
		goto floating
	}

exponent:
	p = unsafe.Add(p, 1)
	if cParityNumberByte(p) == '-' {
		p = unsafe.Add(p, 1)
		expNeg = -1
	} else if cParityNumberByte(p) == '+' {
		p = unsafe.Add(p, 1)
	}
	if !cParityNumberDigit(cParityNumberByte(p)) {
		goto invalid
	}
	for cParityNumberByte(p) == '0' {
		p = unsafe.Add(p, 1)
	}
	count = 0
	for cParityNumberDigit(cParityNumberByte(p)) {
		if count < 12 {
			expDec = expDec*10 + int64(cParityNumberByte(p)-'0')
			count++
		}
		p = unsafe.Add(p, 1)
	}
	if count >= 12 {
		expDec = 1 << 40
	}
	exp += expDec * expNeg

floating:
	if sig == 0 {
		value = 0
		if neg {
			value = -value
		}
		converted = true
	} else if exp >= -348 && exp <= 347 {
		value, converted = cParityAtofFast(sig, int32(exp), neg, trunc)
	}
	if !converted {
		value = cParityAtofFallback(start, int(uintptr(p)-uintptr(start)), &dbuf)
		if neg {
			value = -value
		}
	}
	if math.IsInf(value, 0) {
		code = SONIC_FLOAT_INF
		goto done
	}
	num.typ, num.val = KReal, math.Float64bits(value)
	goto done
invalid:
	code = SONIC_INVALID_NUM
done:
	return p, num, code
}

// cParityAtofFast follows scanning.h, including the truncation ambiguity probe.
// cParitySkipDigits returns the first non-digit at or after p and the number
// of digits skipped. Digits beyond the accumulated significand only move the
// exponent or set truncation, so whole words are skipped while all eight bytes
// are ASCII digits. The parser input is padded and ends in a non-digit, so every
// word read stays readable. A byte at or above 0xfa can carry into its neighbor
// only when its own high nibble already rejects the word.
func cParitySkipDigits(p unsafe.Pointer) (unsafe.Pointer, int64) {
	start := p
	for {
		w := *(*uint64)(p)
		if (w&0xf0f0f0f0f0f0f0f0)|(((w+0x0606060606060606)&0xf0f0f0f0f0f0f0f0)>>4) != 0x3333333333333333 {
			break
		}
		p = unsafe.Add(p, 8)
	}
	for cParityNumberDigit(cParityNumberByte(p)) {
		p = unsafe.Add(p, 1)
	}
	return p, int64(uintptr(p) - uintptr(start))
}

func cParityAtofFast(mant uint64, exp int32, neg, trunc bool) (float64, bool) {
	v := float64(mant)
	if mant>>52 == 0 {
		if neg {
			v = -v
		}
		if exp == 0 || mant == 0 {
			return v, true
		} else if exp > 0 && exp <= 37 {
			e := exp
			if e > 22 {
				v *= cParityPowersOfTen[e-22]
				e = 22
			}
			if v <= 1e15 && v >= -1e15 {
				return v * cParityPowersOfTen[e], true
			}
		} else if exp < 0 && exp >= -22 {
			return v / cParityPowersOfTen[-exp], true
		}
	}
	value, ok := cParityEiselLemire(mant, exp, neg)
	if !ok {
		return 0, false
	}
	if !trunc {
		return value, true
	}
	upper, ok := cParityEiselLemire(mant+1, exp, neg)
	return value, ok && upper == value
}

// Eisel-Lemire float64 conversion using the 128-bit power-of-10 mantissa table;
// the third-party license header is preserved in parse_cparity_number_tables.go.
func cParityEiselLemire(mant uint64, exp int32, neg bool) (float64, bool) {
	if exp < -348 || exp > 347 {
		return 0, false
	}
	clz := bits.LeadingZeros64(mant)
	mant <<= uint(clz)
	exp2 := uint64((217706*exp)>>16) + 64 + 1023 - uint64(clz)
	hi, lo := bits.Mul64(mant, cParityPow10Mantissa[exp+348][1])
	if hi&0x1ff == 0x1ff && lo+mant < mant {
		yhi, ylo := bits.Mul64(mant, cParityPow10Mantissa[exp+348][0])
		mergedHi, mergedLo := hi, lo+yhi
		if mergedLo < lo {
			mergedHi++
		}
		if mergedHi&0x1ff == 0x1ff && mergedLo+1 == 0 && ylo+mant < mant {
			return 0, false
		}
		hi, lo = mergedHi, mergedLo
	}
	msb := hi >> 63
	result := hi >> (msb + 9)
	exp2 -= 1 ^ msb
	if lo == 0 && hi&0x1ff == 0 && result&3 == 1 {
		return 0, false
	}
	result += result & 1
	result >>= 1
	if result>>53 != 0 {
		result >>= 1
		exp2++
	}
	if exp2-1 >= 0x7ff-1 {
		return 0, false
	}
	u := exp2<<52 | result&0x000fffffffffffff
	if neg {
		u |= 1 << 63
	}
	return math.Float64frombits(u), true
}

func cParitySkipPositive(cur *unsafe.Pointer, remain int) int {
	n := cParityDoSkipNumber(*cur, remain)
	if n < 0 {
		*cur = unsafe.Add(*cur, -n-1)
		return -SONIC_INVALID_NUM
	}
	*cur = unsafe.Add(*cur, n)
	return n
}

// Preserve scanning.h's SIMD-block error priority: duplicate decimal, exponent,
// and sign masks are checked before their previously recorded positions.
func cParityDoSkipNumber(start unsafe.Pointer, remain int) int {
	dot, exponent, sign := -1, -1, -1
	pos := 0
	if remain == 0 {
		return -1
	}
	if cParityNumberByte(start) == '0' && (remain == 1 || cParityNumberByte(unsafe.Add(start, 1)) != '.' && cParityNumberByte(unsafe.Add(start, 1)) != 'e' && cParityNumberByte(unsafe.Add(start, 1)) != 'E') {
		return 1
	}
	for remain >= 16 {
		var md, me, ms, valid uint32
		if cpu.UseScalar {
			md, me, ms, valid = cParityNumberMasks16Scalar(unsafe.Add(start, pos))
		} else {
			md, me, ms, valid = cParityNumberMasks16(unsafe.Add(start, pos))
		}
		width := bits.TrailingZeros32(^valid | 1<<16)
		if width != 16 {
			keep := uint32(1)<<uint(width) - 1
			md, me, ms = md&keep, me&keep, ms&keep
		}
		if v := md & (md - 1); v != 0 {
			return -(pos + bits.TrailingZeros32(v) + 1)
		}
		if v := me & (me - 1); v != 0 {
			return -(pos + bits.TrailingZeros32(v) + 1)
		}
		if v := ms & (ms - 1); v != 0 {
			return -(pos + bits.TrailingZeros32(v) + 1)
		}
		if md != 0 {
			at := pos + bits.TrailingZeros32(md)
			if dot != -1 {
				return -at - 1
			}
			dot = at
		}
		if me != 0 {
			at := pos + bits.TrailingZeros32(me)
			if exponent != -1 {
				return -at - 1
			}
			exponent = at
		}
		if ms != 0 {
			at := pos + bits.TrailingZeros32(ms)
			if sign != -1 {
				return -at - 1
			}
			sign = at
		}
		pos += width
		if width != 16 {
			goto check
		}
		remain -= 16
	}
	for remain > 0 {
		remain--
		c := cParityNumberByte(unsafe.Add(start, pos))
		pos++
		switch c {
		case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		case '.':
			if dot != -1 {
				return -pos
			}
			dot = pos - 1
		case 'e', 'E':
			if exponent != -1 {
				return -pos
			}
			exponent = pos - 1
		case '+', '-':
			if sign != -1 {
				return -pos
			}
			sign = pos - 1
		default:
			pos--
			goto check
		}
	}
check:
	if dot == 0 || sign == 0 || exponent == 0 {
		return -1
	} else if dot == pos-1 || sign == pos-1 || exponent == pos-1 {
		return -pos
	} else if sign > 0 && exponent != sign-1 {
		return -sign - 1
	} else if dot >= 0 && exponent >= 0 && dot > exponent-1 {
		return -dot - 1
	} else if dot >= 0 && exponent >= 0 && dot == exponent-1 {
		return -exponent - 1
	}
	return pos
}
