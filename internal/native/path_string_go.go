//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"encoding/binary"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// stringTail scans relaxed JSON string content after its opening quote.
// Bounds checks precede every load.
func (b *pathBlock) stringTail(src string, pos int) (end, escape int, err types.ParsingError) {
	base := unsafe.Pointer(rt.StringData(src))
	sp, se := pos, len(src)
	stop := minNative(se, sp+32)
	escape = -1
	for sp < stop && se-sp >= 8 {
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, sp))[:])
		slash := x ^ ('\\' * tokenByteOnes)
		backslashes := (slash - tokenByteOnes) &^ slash & tokenByteHighBits
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		// Only the first match is used: subtraction may mark later bytes too.
		if (quotes | backslashes) != 0 {
			if backslashes == 0 || (quotes != 0 && bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
				return sp + (bits.TrailingZeros64(quotes) >> 3) + 1, escape, 0
			}
			sp += bits.TrailingZeros64(backslashes) >> 3
			goto scanEscape
		}
		sp += 8
	}
	if sp < stop {
		c := src[sp]
		if c == '"' {
			return sp + 1, escape, 0
		}
		if c == '\\' {
			goto scanEscape
		}
		// Reuse the checked ordinary byte. The remaining one-byte suffix is
		// cheaper to inspect directly; longer tails can amortize classification.
		sp++
		if se-sp >= 2 && sp >= 8 {
			goto scanTail
		}
	}
scanBytes:
	for sp < stop {
		c := src[sp]
		if c == '"' {
			return sp + 1, escape, 0
		}
		if c == '\\' {
			goto scanEscape
		}
		sp++
	}
	if se-sp == 1 && src[sp] == '\\' && escape < 0 {
		escape = sp
	}
	if sp >= se || se-sp == 1 && src[sp] == '\\' {
		return len(src), escape, types.ERR_EOF
	}
	{
		off := sp
		end, nextEscape, err, events, width := kernels.ScanPathStringBlock(src[off:])
		end += off
		b.saveStringTail(end, events, width)
		if escape < 0 && nextEscape >= 0 {
			escape = off + nextEscape
		}
		return end, escape, err
	}

scanEscape:
	// Word and byte scans already located the slash; do not reload it.
	if escape < 0 {
		escape = sp
	}
	if se-sp < 2 {
		return len(src), escape, types.ERR_EOF
	}
	sp += 2
	goto scanBytes

scanTail:
	// Falling out of the word loop before stop leaves fewer than eight bytes.
	// Shift away the consumed prefix before classifying the bounded last word,
	// avoiding both repeated escapes and subtraction's cross-byte borrow flags.
	x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, se-8))[:])
	x >>= (8 - (se - sp)) * 8
	slash := x ^ ('\\' * tokenByteOnes)
	backslashes := (slash - tokenByteOnes) &^ slash & tokenByteHighBits
	q := x ^ ('"' * tokenByteOnes)
	quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
	if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
		return sp + (bits.TrailingZeros64(quotes) >> 3) + 1, escape, 0
	}
	if backslashes == 0 {
		return len(src), escape, types.ERR_EOF
	}
	sp += bits.TrailingZeros64(backslashes) >> 3
	goto scanEscape
}
