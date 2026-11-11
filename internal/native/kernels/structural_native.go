//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package kernels

import (
	"encoding/binary"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

const (
	scLSB64   = 0x0101010101010101
	scMSB64   = 0x8080808080808080
	scQuote64 = 0x2222222222222222
)

// SkipContainer skips the container whose known opening '[' or '{' is at start.
// It returns the position following the matching closing bracket, or len(src)
// and ERR_EOF. It counts only brackets of the opening kind, ignores brackets
// inside strings, and does not validate JSON syntax or impose a depth limit.
// An odd backslash run escapes a following quote even outside a string, matching
// the native relaxed scanner. The source needs no padding and is not modified.
func SkipContainer(src string, start int) (end int, err types.ParsingError) {
	base := start + 1
	tail := src[base:]
	open := src[start]
	close := open + 2
	// Empty containers need no classification or quote/depth state.
	if len(tail) != 0 && tail[0] == close {
		return base + 1, 0
	}
	// Branchless SWAR fast path for short quote-free, flat containers
	// (e.g. numeric/boolean arrays like [-2147483648,2147483647], [-0.1,0.1], [true,true,true]).
	// Quote-containing containers go directly to the branchless SIMD/PCLMULQDQ kernel.
	if len(tail) >= 32 {
		p := unsafe.Pointer(rt.StringData(tail))
		cm := uint64(close) * scLSB64
		om := uint64(open) * scLSB64

		w0 := binary.LittleEndian.Uint64((*[8]byte)(p)[:])
		c0 := w0 ^ cm
		if cm0 := (c0 - scLSB64) & ^c0 & scMSB64; cm0 != 0 {
			q0 := w0 ^ scQuote64
			o0 := w0 ^ om
			qo0 := (((q0 - scLSB64) & ^q0) | ((o0 - scLSB64) & ^o0)) & scMSB64
			firstCloseBit := cm0 & -cm0
			if qo0&(firstCloseBit-1) == 0 {
				return base + (bits.TrailingZeros64(cm0) >> 3) + 1, 0
			}
			end, err = skipContainer(rt.StringData(tail), len(tail), uint64(open))
			return base + end, err
		}

		q0 := w0 ^ scQuote64
		o0 := w0 ^ om
		if ((((q0 - scLSB64) & ^q0) | ((o0 - scLSB64) & ^o0)) & scMSB64) == 0 {
			w1 := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(p, 8))[:])
			c1 := w1 ^ cm
			if cm1 := (c1 - scLSB64) & ^c1 & scMSB64; cm1 != 0 {
				q1 := w1 ^ scQuote64
				o1 := w1 ^ om
				qo1 := (((q1 - scLSB64) & ^q1) | ((o1 - scLSB64) & ^o1)) & scMSB64
				firstCloseBit := cm1 & -cm1
				if qo1&(firstCloseBit-1) == 0 {
					return base + 8 + (bits.TrailingZeros64(cm1) >> 3) + 1, 0
				}
			} else {
				q1 := w1 ^ scQuote64
				o1 := w1 ^ om
				if ((((q1 - scLSB64) & ^q1) | ((o1 - scLSB64) & ^o1)) & scMSB64) == 0 {
					w2 := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(p, 16))[:])
					c2 := w2 ^ cm
					if cm2 := (c2 - scLSB64) & ^c2 & scMSB64; cm2 != 0 {
						q2 := w2 ^ scQuote64
						o2 := w2 ^ om
						qo2 := (((q2 - scLSB64) & ^q2) | ((o2 - scLSB64) & ^o2)) & scMSB64
						firstCloseBit := cm2 & -cm2
						if qo2&(firstCloseBit-1) == 0 {
							return base + 16 + (bits.TrailingZeros64(cm2) >> 3) + 1, 0
						}
					} else {
						q2 := w2 ^ scQuote64
						o2 := w2 ^ om
						if ((((q2 - scLSB64) & ^q2) | ((o2 - scLSB64) & ^o2)) & scMSB64) == 0 {
							w3 := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(p, 24))[:])
							c3 := w3 ^ cm
							if cm3 := (c3 - scLSB64) & ^c3 & scMSB64; cm3 != 0 {
								q3 := w3 ^ scQuote64
								o3 := w3 ^ om
								qo3 := (((q3 - scLSB64) & ^q3) | ((o3 - scLSB64) & ^o3)) & scMSB64
								firstCloseBit := cm3 & -cm3
								if qo3&(firstCloseBit-1) == 0 {
									return base + 24 + (bits.TrailingZeros64(cm3) >> 3) + 1, 0
								}
							}
						}
					}
				}
			}
		}
	}
	end, err = skipContainer(rt.StringData(tail), len(tail), uint64(open))
	return base + end, err
}
