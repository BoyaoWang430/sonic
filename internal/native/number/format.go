package number

import (
	"math"
	"math/bits"
	"unsafe"
)

const (
	float32MantBits = 23
	float32ExpBits  = 8
	float32Bias     = -127
	float32MinExp   = -189

	float64MantBits = 52
	float64ExpBits  = 11
	float64Bias     = -1023
	float64MinExp   = -1085
)

type pmHiLo struct {
	hi uint64
	lo uint64
}

type scaler struct {
	pmHi uint64
	pmLo uint64
	s    int
}

type unrounded uint64

func bool2u64(b bool) uint64 {
	if b {
		return 1
	}
	return 0
}

func bool2int(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (u unrounded) floor() uint64         { return uint64((u + 0) >> 2) }
func (u unrounded) round() uint64         { return uint64((u + 1 + (u>>2)&1) >> 2) }
func (u unrounded) ceil() uint64          { return uint64((u + 3) >> 2) }
func (u unrounded) nudge(d int) unrounded { return u + unrounded(d) }

func unmin(x uint64) unrounded {
	return unrounded(x<<2 - 2)
}

func log10Pow2(x int) int {
	return (x * 78913) >> 18
}

func log2Pow10(x int) int {
	return (x * 108853) >> 15
}

func skewed(e int) int {
	return (e*631305 - 261663) >> 21
}

var uint64pow10 = [...]uint64{
	1, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9,
	1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19,
}

const smalls = "00010203040506070809" +
	"10111213141516171819" +
	"20212223242526272829" +
	"30313233343536373839" +
	"40414243444546474849" +
	"50515253545556575859" +
	"60616263646566676869" +
	"70717273747576777879" +
	"80818283848586878889" +
	"90919293949596979899"

func prescale(pre *scaler, e, p, lp int) {
	pre.pmHi = pow10Tab[p-pow10Min].hi
	pre.pmLo = pow10Tab[p-pow10Min].lo
	pre.s = -(e + lp + 3)
}

func uscale(x uint64, c *scaler) unrounded {
	hi, mid := bits.Mul64(x, c.pmHi)
	s := uint(c.s) & 63
	if hi>>s<<s != hi {
		return unrounded(hi>>s | 1)
	}
	mid2, _ := bits.Mul64(x, c.pmLo)
	hi -= bool2u64(mid < mid2)
	return unrounded(hi>>s | bool2u64(mid-mid2 > 1))
}

func shortFloat(m uint64, e int, mantBits, minExp int) (d uint64, p int) {
	var min, max uint64
	var odd int
	z := 63 - mantBits
	if m == 1<<63 && e > minExp {
		p = -skewed(e + z)
		min = m - 1<<(uint(z)-2)
		max = m + 1<<(uint(z)-1)
		odd = int(m>>uint(z)) & 1
	} else {
		if e < minExp {
			z = z + (minExp - e)
		}
		p = -log10Pow2(e + z)
		min = m - 1<<(uint(z)-1)
		max = m + 1<<(uint(z)-1)
		odd = int(m>>uint(z)) & 1
	}

	var pre scaler
	prescale(&pre, e, p, log2Pow10(p))
	dmin := uscale(min, &pre).nudge(+odd).ceil()
	dmax := uscale(max, &pre).nudge(-odd).floor()

	if d = dmax / 10; d*10 >= dmin {
		return d, -(p - 1)
	}
	if d = dmin; d < dmax {
		d = uscale(m, &pre).round()
	}
	return d, -p
}

func numDigits(d uint64) int {
	nd := log10Pow2(bits.Len64(d))
	return nd + bool2int(d >= uint64pow10[nd])
}

func formatBase10(a []byte, u uint64) {
	nd := len(a)
	for nd > 8 {
		x3210 := uint32(u % 1e8)
		u /= 1e8
		x32, x10 := x3210/1e4, x3210%1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		x3, x2 := (x32/100)*2, (x32%100)*2
		a[nd-1], a[nd-2] = smalls[x0+1], smalls[x0]
		a[nd-3], a[nd-4] = smalls[x1+1], smalls[x1]
		a[nd-5], a[nd-6] = smalls[x2+1], smalls[x2]
		a[nd-7], a[nd-8] = smalls[x3+1], smalls[x3]
		nd -= 8
	}
	x := uint32(u)
	if nd == 8 {
		x32, x10 := x/1e4, x%1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		x3, x2 := (x32/100)*2, (x32%100)*2
		a[7], a[6] = smalls[x0+1], smalls[x0]
		a[5], a[4] = smalls[x1+1], smalls[x1]
		a[3], a[2] = smalls[x2+1], smalls[x2]
		a[1], a[0] = smalls[x3+1], smalls[x3]
		return
	}
	if nd >= 4 {
		x10 := x % 1e4
		x /= 1e4
		x1, x0 := (x10/100)*2, (x10%100)*2
		a[nd-1], a[nd-2] = smalls[x0+1], smalls[x0]
		a[nd-3], a[nd-4] = smalls[x1+1], smalls[x1]
		nd -= 4
	}
	if nd >= 2 {
		x0 := (x % 1e2) * 2
		x /= 1e2
		a[nd-1], a[nd-2] = smalls[x0+1], smalls[x0]
		nd -= 2
	}
	if nd > 0 {
		a[0] = byte('0' + x)
	}
}

// AppendFloat appends the shortest JSON representation of v at the requested
// precision, which must be 32 or 64 bits. It uses fixed notation from 1e-6 up to
// (but excluding) 1e21, and scientific notation elsewhere, without a redundant
// exponent zero. For 32 bits, v is first rounded to float32.
func AppendFloat(dst []byte, v float64, bitSize int) []byte {
	if bitSize == 64 {
		return AppendFloat64(dst, v)
	}
	if bitSize == 32 {
		return AppendFloat32(dst, float32(v))
	}
	panic("number.AppendFloat: bits must be 32 or 64")
}

var zero32 [32]byte

const (
	float64FixedMinBits = 0x3eb0c6f7a0b5ed8d // math.Float64bits(1e-6)
	float64FixedMaxBits = 0x444b1ae4d6e2ef50 // math.Float64bits(1e21)
	float32FixedMinBits = 0x358637bd         // math.Float32bits(1e-6)
	float32FixedMaxBits = 0x6258d727         // math.Float32bits(1e21)
)

func AppendFloat64(dst []byte, v float64) []byte {
	b := math.Float64bits(v)
	exp := int(b>>float64MantBits) & (1<<float64ExpBits - 1)
	if exp == 1<<float64ExpBits-1 {
		return dst
	}
	neg := b>>63 != 0
	mant := b & (1<<float64MantBits - 1)

	n := len(dst)
	if cap(dst)-n < 32 {
		dst = append(dst, zero32[:]...)[:n]
	}
	buf := dst[n : n+32]
	pos := 0
	if neg {
		buf[0] = '-'
		pos = 1
	}
	if exp == 0 {
		if mant == 0 {
			buf[pos] = '0'
			return dst[:n+pos+1]
		}
		exp = 1 + float64Bias
	} else {
		mant |= 1 << float64MantBits
		exp += float64Bias
		if uint(exp) <= float64MantBits {
			shift := uint(float64MantBits - exp)
			if mant&((1<<shift)-1) == 0 {
				u := mant >> shift
				if u < 10 {
					buf[pos] = byte('0' + u)
					return dst[:n+pos+1]
				}
				if u < 100 {
					x := u * 2
					buf[pos] = smalls[x]
					buf[pos+1] = smalls[x+1]
					return dst[:n+pos+2]
				}
				nd := numDigits(u)
				formatBase10(buf[pos:pos+nd], u)
				return dst[:n+pos+nd]
			}
		} else if exp == float64MantBits+1 && mant == 1<<float64MantBits {
			copy(buf[pos:], "9007199254740992")
			return dst[:n+pos+16]
		}
	}

	s := 64 - bits.Len64(mant)
	d, p := shortFloat(mant<<uint(s), exp-s-float64MantBits, float64MantBits, float64MinExp)
	absBits := b & (1<<63 - 1)
	return formatDecimal(dst, buf, n, pos, d, p, absBits >= float64FixedMinBits && absBits < float64FixedMaxBits)
}

func AppendFloat32(dst []byte, f float32) []byte {
	b := math.Float32bits(f)
	exp := int(b>>float32MantBits) & (1<<float32ExpBits - 1)
	if exp == 1<<float32ExpBits-1 {
		return dst
	}
	neg := b>>31 != 0
	mant := uint64(b & (1<<float32MantBits - 1))

	n := len(dst)
	if cap(dst)-n < 32 {
		dst = append(dst, zero32[:]...)[:n]
	}
	buf := dst[n : n+32]
	pos := 0
	if neg {
		buf[0] = '-'
		pos = 1
	}
	if exp == 0 {
		if mant == 0 {
			buf[pos] = '0'
			return dst[:n+pos+1]
		}
		exp = 1 + float32Bias
	} else {
		mant |= 1 << float32MantBits
		exp += float32Bias
		if uint(exp) <= float32MantBits {
			shift := uint(float32MantBits - exp)
			if mant&((1<<shift)-1) == 0 {
				u := mant >> shift
				if u < 10 {
					buf[pos] = byte('0' + u)
					return dst[:n+pos+1]
				}
				if u < 100 {
					x := u * 2
					buf[pos] = smalls[x]
					buf[pos+1] = smalls[x+1]
					return dst[:n+pos+2]
				}
				nd := numDigits(u)
				formatBase10(buf[pos:pos+nd], u)
				return dst[:n+pos+nd]
			}
		} else if exp == float32MantBits+1 && mant == 1<<float32MantBits {
			copy(buf[pos:], "16777216")
			return dst[:n+pos+8]
		}
	}

	s := 64 - bits.Len64(mant)
	d, p := shortFloat(mant<<uint(s), exp-s-float32MantBits, float32MantBits, float32MinExp)
	absBits := b & (1<<31 - 1)
	return formatDecimal(dst, buf, n, pos, d, p, absBits >= float32FixedMinBits && absBits < float32FixedMaxBits)
}

func formatDecimal(dst, buf []byte, n, pos int, d uint64, p int, fixed bool) []byte {
	nd := numDigits(d)
	dp := nd + p
	if fixed {
		if dp >= nd {
			formatBase10(buf[pos:pos+nd], d)
			end := pos + dp
			for pos += nd; pos < end; pos++ {
				buf[pos] = '0'
			}
			return dst[:n+end]
		}
		if dp > 0 {
			formatBase10(buf[pos+1:pos+1+nd], d)
			switch dp {
			case 1:
				buf[pos] = buf[pos+1]
				buf[pos+1] = '.'
			case 2:
				buf[pos], buf[pos+1] = buf[pos+1], buf[pos+2]
				buf[pos+2] = '.'
			default:
				for i := 0; i < dp; i++ {
					buf[pos+i] = buf[pos+1+i]
				}
				buf[pos+dp] = '.'
			}
			end := pos + 1 + nd
			for buf[end-1] == '0' {
				end--
			}
			if buf[end-1] == '.' {
				end--
			}
			return dst[:n+end]
		}
		buf[pos] = '0'
		buf[pos+1] = '.'
		zEnd := pos + 2 - dp
		for i := pos + 2; i < zEnd; i++ {
			buf[i] = '0'
		}
		end := zEnd + nd
		formatBase10(buf[zEnd:end], d)
		for buf[end-1] == '0' {
			end--
		}
		return dst[:n+end]
	}

	if nd == 1 {
		buf[pos] = byte('0' + d)
		pos++
	} else {
		formatBase10(buf[pos+1:pos+1+nd], d)
		buf[pos] = buf[pos+1]
		buf[pos+1] = '.'
		end := pos + 1 + nd
		for buf[end-1] == '0' {
			end--
		}
		if buf[end-1] == '.' {
			end--
		}
		pos = end
	}
	buf[pos] = 'e'
	exp10 := dp - 1
	if exp10 < 0 {
		buf[pos+1] = '-'
		exp10 = -exp10
		pos += 2
		if exp10 < 10 {
			buf[pos] = byte(exp10) + '0'
			return dst[:n+pos+1]
		}
	} else {
		buf[pos+1] = '+'
		pos += 2
	}
	if exp10 < 100 {
		x := exp10 * 2
		buf[pos] = smalls[x]
		buf[pos+1] = smalls[x+1]
		return dst[:n+pos+2]
	}
	q := exp10 / 100
	r := (exp10 % 100) * 2
	buf[pos] = byte(q) + '0'
	buf[pos+1] = smalls[r]
	buf[pos+2] = smalls[r+1]
	return dst[:n+pos+3]
}

func FormatUint64(out *byte, v uint64) int {
	buf := unsafe.Slice(out, 32)
	if v < 10 {
		buf[0] = byte(v) + '0'
		return 1
	}
	if v < 100 {
		idx := v * 2
		buf[0] = smalls[idx]
		buf[1] = smalls[idx+1]
		return 2
	}
	nd := numDigits(v)
	formatBase10(buf[:nd], v)
	return nd
}

func FormatInt64(out *byte, v int64) int {
	buf := unsafe.Slice(out, 32)
	u := uint64(v)
	pos := 0
	if v < 0 {
		buf[0] = '-'
		pos = 1
		u = uint64(-v)
	}
	if u < 10 {
		buf[pos] = byte(u) + '0'
		return pos + 1
	}
	if u < 100 {
		idx := u * 2
		buf[pos] = smalls[idx]
		buf[pos+1] = smalls[idx+1]
		return pos + 2
	}
	nd := numDigits(u)
	formatBase10(buf[pos:pos+nd], u)
	return pos + nd
}
