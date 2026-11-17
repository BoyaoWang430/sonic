package number

import (
	"errors"
	"math"
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

// ParseValidJSONFloat64 converts an already syntax-validated JSON number token
// to IEEE-754 float64. For tokens longer than 800 bytes it bypasses older Go
// toolchains' strconv decimal-point truncation bug. On overflow it returns
// signed +/-Inf and ok=false.
func ParseValidJSONFloat64(s string) (float64, bool) {
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
