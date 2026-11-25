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

package number

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
)

const decimalBufCap = 800

var decimalPowTable = [...]int32{1, 3, 6, 9, 13, 16, 19, 23, 26}

var leftShiftTable = [...]struct {
	delta  int32
	cutoff string
}{
	{0, ""},
	{1, "5"},
	{1, "25"},
	{1, "125"},
	{2, "625"},
	{2, "3125"},
	{2, "15625"},
	{3, "78125"},
	{3, "390625"},
	{3, "1953125"},
	{4, "9765625"},
	{4, "48828125"},
	{4, "244140625"},
	{4, "1220703125"},
	{5, "6103515625"},
	{5, "30517578125"},
	{5, "152587890625"},
	{6, "762939453125"},
	{6, "3814697265625"},
	{6, "19073486328125"},
	{7, "95367431640625"},
	{7, "476837158203125"},
	{7, "2384185791015625"},
	{7, "11920928955078125"},
	{8, "59604644775390625"},
	{8, "298023223876953125"},
	{8, "1490116119384765625"},
	{9, "7450580596923828125"},
	{9, "37252902984619140625"},
	{9, "186264514923095703125"},
	{10, "931322574615478515625"},
	{10, "4656612873077392578125"},
	{10, "23283064365386962890625"},
	{10, "116415321826934814453125"},
	{11, "582076609134674072265625"},
	{11, "2910383045673370361328125"},
	{11, "14551915228366851806640625"},
	{12, "72759576141834259033203125"},
	{12, "363797880709171295166015625"},
	{12, "1818989403545856475830078125"},
	{13, "9094947017729282379150390625"},
	{13, "45474735088646411895751953125"},
	{13, "227373675443232059478759765625"},
	{13, "1136868377216160297393798828125"},
	{14, "5684341886080801486968994140625"},
	{14, "28421709430404007434844970703125"},
	{14, "142108547152020037174224853515625"},
	{15, "710542735760100185871124267578125"},
	{15, "3552713678800500929355621337890625"},
	{15, "17763568394002504646778106689453125"},
	{16, "88817841970012523233890533447265625"},
	{16, "444089209850062616169452667236328125"},
	{16, "2220446049250313080847263336181640625"},
	{16, "11102230246251565404236316680908203125"},
	{17, "55511151231257827021181583404541015625"},
	{17, "277555756156289135105907917022705078125"},
	{17, "1387778780781445675529539585113525390625"},
	{18, "6938893903907228377647697925567626953125"},
	{18, "34694469519536141888238489627838134765625"},
	{18, "173472347597680709441192448139190673828125"},
	{19, "867361737988403547205962240695953369140625"},
}

// boundedDecimal implements exact IEEE-754 decimal-to-binary64 and binary32
// round-to-even conversion. Every binary64 midpoint has <= 767 significant
// decimal digits, so 800 stored digits plus an exact logical decimal point (dp)
// and nonzero tail flag (trunc) yield exact single-step rounding for arbitrarily
// long decimal tokens independent of the Go toolchain version.
type boundedDecimal struct {
	d      []byte
	nd, dp int32
	neg    bool
	trunc  bool
}

func (d *boundedDecimal) set(s string) bool {
	var dp int64
	i := 0
	if i >= len(s) {
		return false
	}
	switch s[i] {
	case '+':
		i++
	case '-':
		d.neg = true
		i++
	}

	sawDot := false
	sawDigits := false
	capDigits := len(d.d)
	for ; i < len(s); i++ {
		c := s[i]
		if c == '_' {
			continue
		}
		if c == '.' {
			if sawDot {
				return false
			}
			sawDot = true
			continue
		}
		if c >= '0' && c <= '9' {
			sawDigits = true
			if c == '0' && d.nd == 0 {
				if sawDot {
					dp--
				}
				continue
			}
			if !sawDot {
				dp++
			}
			if int(d.nd) < capDigits {
				d.d[d.nd] = c
				d.nd++
			} else if c != '0' {
				d.trunc = true
			}
			continue
		}
		break
	}
	if !sawDigits {
		return false
	}

	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i >= len(s) {
			return false
		}
		expSign := int64(1)
		switch s[i] {
		case '+':
			i++
		case '-':
			expSign = -1
			i++
		}
		sawExp := false
		var exp int64
		const maxExp = int64(1) << 40
		for ; i < len(s); i++ {
			c := s[i]
			if c == '_' {
				continue
			}
			if c < '0' || c > '9' {
				return false
			}
			sawExp = true
			if exp < maxExp {
				exp = exp*10 + int64(c-'0')
			}
		}
		if !sawExp {
			return false
		}
		dp += expSign * exp
	}
	if i != len(s) {
		return false
	}

	d.trim()
	if d.nd == 0 {
		d.dp = 0
	} else if dp > 310 {
		d.dp = 311
	} else if dp < -330 {
		d.dp = -331
	} else {
		d.dp = int32(dp)
	}
	return true
}

func (d *boundedDecimal) trim() {
	for d.nd > 0 && d.d[d.nd-1] == '0' {
		d.nd--
	}
	if d.nd == 0 {
		d.dp = 0
	}
}

func (d *boundedDecimal) rightShift(k uint32) {
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
		n = n*10 + uint64(d.d[r]) - '0'
	}
	d.dp -= r - 1
	mask := uint64(1)<<k - 1
	for ; r < d.nd; r++ {
		digit := n >> k
		n &= mask
		d.d[w] = byte(digit + '0')
		w++
		n = n*10 + uint64(d.d[r]) - '0'
	}
	capDigits := len(d.d)
	for n > 0 {
		digit := n >> k
		n &= mask
		if int(w) < capDigits {
			d.d[w] = byte(digit + '0')
			w++
		} else if digit > 0 {
			d.trunc = true
		}
		n *= 10
	}
	d.nd = w
	d.trim()
}

func (d *boundedDecimal) prefixLess(cutoff string) bool {
	for i := int32(0); i < d.nd; i++ {
		if int(i) == len(cutoff) {
			return false
		}
		if d.d[i] != cutoff[i] {
			return d.d[i] < cutoff[i]
		}
	}
	return int(d.nd) < len(cutoff)
}

func (d *boundedDecimal) leftShift(k uint32) {
	entry := leftShiftTable[k]
	delta := entry.delta
	if d.prefixLess(entry.cutoff) {
		delta--
	}
	r, w := d.nd, d.nd+delta
	capDigits := len(d.d)
	var n uint64
	for r--; r >= 0; r-- {
		n += uint64(d.d[r]-'0') << k
		quotient := n / 10
		remainder := n - 10*quotient
		w--
		if int(w) < capDigits {
			d.d[w] = byte(remainder + '0')
		} else if remainder != 0 {
			d.trunc = true
		}
		n = quotient
	}
	for n > 0 {
		quotient := n / 10
		remainder := n - 10*quotient
		w--
		if int(w) < capDigits {
			d.d[w] = byte(remainder + '0')
		} else if remainder != 0 {
			d.trunc = true
		}
		n = quotient
	}
	d.nd += delta
	if int(d.nd) >= capDigits {
		d.nd = int32(capDigits)
	}
	d.dp += delta
	d.trim()
}

func (d *boundedDecimal) shift(k int32) {
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

func (d *boundedDecimal) shouldRoundUp(nd int32) bool {
	if nd < 0 || nd >= d.nd {
		return false
	}
	if d.d[nd] == '5' && nd+1 == d.nd {
		if d.trunc {
			return true
		}
		return nd > 0 && (d.d[nd-1]-'0')%2 != 0
	}
	return d.d[nd] >= '5'
}

func (d *boundedDecimal) roundedInteger() uint64 {
	if d.dp > 20 {
		return ^uint64(0)
	}
	i := int32(0)
	var n uint64
	for ; i < d.dp && i < d.nd; i++ {
		n = n*10 + uint64(d.d[i]-'0')
	}
	for ; i < d.dp; i++ {
		n *= 10
	}
	if d.shouldRoundUp(d.dp) {
		n++
	}
	return n
}

func (d *boundedDecimal) floatBits(mantBits, expBits uint, bias, maxDP, minDP int32) (uint64, bool) {
	var exp2 int32
	var mant uint64
	overflow := false

	if d.nd == 0 || d.dp < minDP {
		exp2 = bias
		goto out
	}
	if d.dp > maxDP {
		goto over
	}

	for d.dp > 0 {
		n := int32(27)
		if int(d.dp) < len(decimalPowTable) {
			n = decimalPowTable[d.dp]
		}
		d.shift(-n)
		exp2 += n
	}
	for d.dp < 0 || (d.dp == 0 && d.d[0] < '5') {
		n := int32(27)
		if int(-d.dp) < len(decimalPowTable) {
			n = decimalPowTable[-d.dp]
		}
		d.shift(n)
		exp2 -= n
	}

	exp2--
	if exp2 < bias+1 {
		n := bias + 1 - exp2
		d.shift(-n)
		exp2 += n
	}
	if exp2-bias >= (int32(1)<<expBits)-1 {
		goto over
	}

	d.shift(int32(1 + mantBits))
	mant = d.roundedInteger()
	if mant == uint64(2)<<mantBits {
		mant >>= 1
		exp2++
		if exp2-bias >= (int32(1)<<expBits)-1 {
			goto over
		}
	}
	if mant&(uint64(1)<<mantBits) == 0 {
		exp2 = bias
	}
	goto out

over:
	mant = 0
	exp2 = (int32(1)<<expBits - 1) + bias
	overflow = true

out:
	expMask := uint64(1)<<expBits - 1
	mantMask := uint64(1)<<mantBits - 1
	u := (mant & mantMask) | (uint64(exp2-bias)&expMask)<<mantBits
	if d.neg {
		u |= uint64(1) << (mantBits + expBits)
	}
	return u, overflow
}

// ParseDecimalSliceFloat64 converts a syntactically valid decimal float token
// to float64 using scratch if cap(scratch) >= 800, or a stack buffer otherwise.
// Overflow returns signed +/-Inf.
func ParseDecimalSliceFloat64(s string, scratch []byte) float64 {
	var stack [decimalBufCap]byte
	var buf []byte
	if cap(scratch) >= decimalBufCap {
		buf = scratch[:decimalBufCap]
	} else {
		buf = stack[:]
	}
	d := boundedDecimal{d: buf}
	if !d.set(s) {
		return math.NaN()
	}
	bits, _ := d.floatBits(52, 11, -1023, 310, -330)
	return math.Float64frombits(bits)
}

func parseExactLongDecimalFloat64(s string) (float64, bool) {
	var stack [decimalBufCap]byte
	d := boundedDecimal{d: stack[:]}
	if !d.set(s) {
		return 0, false
	}
	bits, overflow := d.floatBits(52, 11, -1023, 310, -330)
	return math.Float64frombits(bits), !overflow
}

func parseExactLongDecimalFloat32(s string) (float32, bool) {
	var stack [decimalBufCap]byte
	d := boundedDecimal{d: stack[:]}
	if !d.set(s) {
		return 0, false
	}
	bits, overflow := d.floatBits(23, 8, -127, 40, -50)
	return math.Float32frombits(uint32(bits)), !overflow
}

var exactPowersOfTen32 = [...]float32{
	1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10,
}

func parseFastDecimal19(s string) (mant uint64, exp10 int, neg bool, ok bool) {
	if len(s) == 0 || len(s) > 32 {
		return 0, 0, false, false
	}
	i := 0
	if s[0] == '-' {
		neg = true
		i = 1
		if len(s) == 1 {
			return 0, 0, false, false
		}
	}
	c0 := s[i]
	nd := 0
	if c0 == '0' {
		i++
		if i == len(s) {
			return 0, 0, neg, true
		}
		if s[i] >= '0' && s[i] <= '9' {
			return 0, 0, false, false
		}
	} else if c0 >= '1' && c0 <= '9' {
		mant = uint64(c0 - '0')
		nd = 1
		i++
		for i < len(s) {
			d := s[i] - '0'
			if d > 9 {
				break
			}
			if nd >= 19 {
				return 0, 0, false, false
			}
			mant = mant*10 + uint64(d)
			nd++
			i++
		}
	} else {
		return 0, 0, false, false
	}

	if i < len(s) && s[i] == '.' {
		i++
		fracStart := i
		if mant == 0 {
			for i < len(s) && s[i] == '0' {
				exp10--
				i++
			}
		}
		for i < len(s) {
			d := s[i] - '0'
			if d > 9 {
				break
			}
			if nd >= 19 {
				return 0, 0, false, false
			}
			mant = mant*10 + uint64(d)
			nd++
			exp10--
			i++
		}
		if i == fracStart {
			return 0, 0, false, false
		}
	}

	if i < len(s) && (s[i]|0x20) == 'e' {
		i++
		expNeg := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNeg = s[i] == '-'
			i++
		}
		expStart := i
		var e int
		for i < len(s) {
			d := s[i] - '0'
			if d > 9 {
				break
			}
			if e < 10000 {
				e = e*10 + int(d)
			}
			i++
		}
		if i == expStart {
			return 0, 0, false, false
		}
		if expNeg {
			exp10 -= e
		} else {
			exp10 += e
		}
	}

	if i != len(s) {
		return 0, 0, false, false
	}
	return mant, exp10, neg, true
}

func fastJSONFloat32(s string) (float32, bool, bool) {
	mant, exp10, neg, parsed := parseFastDecimal19(s)
	if !parsed {
		return 0, false, false
	}
	if mant == 0 {
		if neg {
			return math.Float32frombits(1 << 31), true, true
		}
		return 0, true, true
	}
	if mant < (1 << 24) {
		if exp10 == 0 {
			fv := float32(mant)
			if neg {
				fv = -fv
			}
			return fv, true, true
		}
		if exp10 > 0 && exp10 <= 10 {
			fv := float32(mant) * exactPowersOfTen32[exp10]
			if neg {
				fv = -fv
			}
			return fv, true, true
		}
		if exp10 < 0 && exp10 >= -10 {
			fv := float32(mant) / exactPowersOfTen32[-exp10]
			if neg {
				fv = -fv
			}
			return fv, true, true
		}
	}
	if exp10 < -37 || exp10 > 19 {
		return 0, false, false
	}
	lz := bits.LeadingZeros64(mant)
	x := mant << uint(lz)
	lp := log2Pow10(exp10)
	hi, _ := bits.Mul64(x, pow10Tab[exp10-pow10Min].hi)
	msb := hi >> 63
	shift := uint(39 + msb)
	mask := (uint64(1) << shift) - 1
	half := uint64(1) << (shift - 1)
	rem := hi & mask
	mant24 := hi >> shift
	exp2 := lp + 63 + int(msb) - lz
	if rem >= half+2 && rem <= mask-1 {
		mant24++
		if mant24 == (uint64(1) << 24) {
			mant24 >>= 1
			exp2++
		}
	} else if !(rem >= 2 && rem <= half-2) {
		return 0, false, false
	}
	biased := exp2 + 127
	if biased <= 0 || biased >= 255 {
		return 0, false, false
	}
	u := (uint32(biased) << 23) | uint32(mant24&((1<<23)-1))
	if neg {
		u |= uint32(1) << 31
	}
	return math.Float32frombits(u), true, true
}

// ConvertExactMantFloat32 converts (-1)^neg * mant * 10^(-frac) (with 0 < mant < 2^53
// and 1 <= frac <= 22) to IEEE-754 float32 with exact single-step round-to-even,
// avoiding re-parsing the JSON source string while never double-rounding through float64.
func ConvertExactMantFloat32(mant uint64, frac int, neg bool) float32 {
	exp10 := -frac
	lz := bits.LeadingZeros64(mant)
	x := mant << uint(lz)
	lp := log2Pow10(exp10)
	hi, _ := bits.Mul64(x, pow10Tab[exp10-pow10Min].hi)
	msb := hi >> 63
	shift := uint(39 + msb)
	mask := (uint64(1) << shift) - 1
	half := uint64(1) << (shift - 1)
	rem := hi & mask
	mant24 := hi >> shift
	exp2 := lp + 63 + int(msb) - lz
	if rem >= half+2 && rem <= mask-1 {
		mant24++
		if mant24 == (uint64(1) << 24) {
			mant24 >>= 1
			exp2++
		}
		biased := exp2 + 127
		if biased > 0 && biased < 255 {
			u := (uint32(biased) << 23) | uint32(mant24&((1<<23)-1))
			if neg {
				u |= uint32(1) << 31
			}
			return math.Float32frombits(u)
		}
	} else if rem >= 2 && rem <= half-2 {
		biased := exp2 + 100 + 27
		if biased > 0 && biased < 255 {
			u := (uint32(biased) << 23) | uint32(mant24&((1<<23)-1))
			if neg {
				u |= uint32(1) << 31
			}
			return math.Float32frombits(u)
		}
	}
	return convertExactMantFloat32Midpoint(mant, frac, neg)
}

//go:noinline
func convertExactMantFloat32Midpoint(mant uint64, frac int, neg bool) float32 {
	var raw [32]byte
	b := raw[:0]
	if neg {
		b = append(b, '-')
	}
	b = strconv.AppendUint(b, mant, 10)
	b = append(b, 'e', '-')
	b = strconv.AppendInt(b, int64(frac), 10)
	f, _ := strconv.ParseFloat(string(b), 32)
	return float32(f)
}

// ParseValidJSONFloat64 converts an already syntax-validated JSON number token
// to IEEE-754 float64. For tokens longer than 800 bytes it bypasses older Go
// toolchains' strconv decimal-point truncation bug. On overflow it returns
// signed +/-Inf and ok=false.
func ParseValidJSONFloat64(s string) (float64, bool) {
	if len(s) > 0 && len(s) <= 32 {
		i := 0
		neg := false
		if s[0] == '-' {
			neg = true
			i = 1
			if len(s) == 1 {
				goto fallback64
			}
		}
		var mant uint64
		exp10 := 0
		nd := 0
		c0 := s[i]
		if c0 == '0' {
			i++
			if i == len(s) {
				if neg {
					return math.Float64frombits(1 << 63), true
				}
				return 0, true
			}
			if s[i] >= '0' && s[i] <= '9' {
				goto fallback64
			}
		} else if c0 >= '1' && c0 <= '9' {
			mant = uint64(c0 - '0')
			nd = 1
			i++
			for i < len(s) {
				d := s[i] - '0'
				if d > 9 {
					break
				}
				if nd >= 19 {
					goto fallback64
				}
				mant = mant*10 + uint64(d)
				nd++
				i++
			}
		} else {
			goto fallback64
		}
		if i < len(s) && s[i] == '.' {
			i++
			fracStart := i
			if mant == 0 {
				for i < len(s) && s[i] == '0' {
					exp10--
					i++
				}
			}
			for i < len(s) {
				d := s[i] - '0'
				if d > 9 {
					break
				}
				if nd >= 19 {
					goto fallback64
				}
				mant = mant*10 + uint64(d)
				nd++
				exp10--
				i++
			}
			if i == fracStart {
				goto fallback64
			}
		}
		if i < len(s) && (s[i]|0x20) == 'e' {
			i++
			expNeg := false
			if i < len(s) && (s[i] == '+' || s[i] == '-') {
				expNeg = s[i] == '-'
				i++
			}
			expStart := i
			var e int
			for i < len(s) {
				d := s[i] - '0'
				if d > 9 {
					break
				}
				if e < 10000 {
					e = e*10 + int(d)
				}
				i++
			}
			if i == expStart {
				goto fallback64
			}
			if expNeg {
				exp10 -= e
			} else {
				exp10 += e
			}
		}
		if i == len(s) {
			if mant == 0 {
				if neg {
					return math.Float64frombits(1 << 63), true
				}
				return 0, true
			}
			if mant < (1 << 53) {
				if exp10 == 0 {
					fv := float64(mant)
					if neg {
						fv = -fv
					}
					return fv, true
				}
				if exp10 > 0 && exp10 <= 22 {
					fv := float64(mant) * exactPowersOfTen[exp10]
					if neg {
						fv = -fv
					}
					return fv, true
				}
				if exp10 < 0 && exp10 >= -22 {
					fv := float64(mant) / exactPowersOfTen[-exp10]
					if neg {
						fv = -fv
					}
					return fv, true
				}
			}
			if exp10 >= -307 && exp10 <= 288 {
				lz := bits.LeadingZeros64(mant)
				x := mant << uint(lz)
				lp := log2Pow10(exp10)
				hi, _ := bits.Mul64(x, pow10Tab[exp10-pow10Min].hi)
				msb := hi >> 63
				shift := uint(10 + msb)
				mask := (uint64(1) << shift) - 1
				half := uint64(1) << (shift - 1)
				rem := hi & mask
				mant53 := hi >> shift
				exp2 := lp + 63 + int(msb) - lz
				if rem >= half+2 && rem <= mask-1 {
					mant53++
					if mant53 == (uint64(1) << 53) {
						mant53 >>= 1
						exp2++
					}
					biased := exp2 + 1023
					if biased > 0 && biased < 2047 {
						u := (uint64(biased) << 52) | (mant53 & ((uint64(1) << 52) - 1))
						if neg {
							u |= uint64(1) << 63
						}
						return math.Float64frombits(u), true
					}
				} else if rem >= 2 && rem <= half-2 {
					biased := exp2 + 1023
					if biased > 0 && biased < 2047 {
						u := (uint64(biased) << 52) | (mant53 & ((uint64(1) << 52) - 1))
						if neg {
							u |= uint64(1) << 63
						}
						return math.Float64frombits(u), true
					}
				}
			}
		}
	}
fallback64:
	if len(s) <= decimalBufCap {
		v, err := strconv.ParseFloat(s, 64)
		return v, err == nil && !math.IsInf(v, 0) && !math.IsNaN(v)
	}
	return parseExactLongDecimalFloat64(s)
}

// ParseValidJSONFloat32 converts an already syntax-validated JSON number token
// to IEEE-754 float32 with single-step round-to-even. On overflow it returns
// (0, false).
func ParseValidJSONFloat32(s string) (float32, bool) {
	if v, ok, handled := fastJSONFloat32(s); handled {
		return v, ok
	}
	if len(s) <= decimalBufCap {
		f, err := strconv.ParseFloat(s, 32)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return float32(f), true
	}
	v, ok := parseExactLongDecimalFloat32(s)
	if !ok {
		return 0, false
	}
	return v, true
}

func isPlainDecimalFloatSyntax(s string) bool {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') {
		return false
	}
	if i < len(s) && (s[i] == 'i' || s[i] == 'I' || s[i] == 'n' || s[i] == 'N') {
		return false
	}
	return i < len(s)
}

// ParseGoFloat64 parses s according to Go's strconv.ParseFloat(s, 64) grammar
// (used by ,string options and float64 map keys) while guaranteeing exact
// >800-digit decimal conversion even on pre-Go 1.27 toolchains.
func ParseGoFloat64(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if len(s) <= decimalBufCap || (err != nil && !errors.Is(err, strconv.ErrRange)) || !isPlainDecimalFloatSyntax(s) {
		return v, err
	}
	exact, ok := parseExactLongDecimalFloat64(s)
	if !ok {
		return exact, &strconv.NumError{Func: "ParseFloat", Num: s, Err: strconv.ErrRange}
	}
	return exact, nil
}

// ParseGoFloat32 parses s according to Go's strconv.ParseFloat(s, 32) grammar
// while guaranteeing exact >800-digit decimal conversion on all Go toolchains.
func ParseGoFloat32(s string) (float32, error) {
	v, err := strconv.ParseFloat(s, 32)
	if len(s) <= decimalBufCap || (err != nil && !errors.Is(err, strconv.ErrRange)) || !isPlainDecimalFloatSyntax(s) {
		return float32(v), err
	}
	exact, ok := parseExactLongDecimalFloat32(s)
	if !ok {
		sign := 1
		if len(s) > 0 && s[0] == '-' {
			sign = -1
		}
		return float32(math.Inf(sign)), &strconv.NumError{Func: "ParseFloat", Num: s, Err: strconv.ErrRange}
	}
	return exact, nil
}
