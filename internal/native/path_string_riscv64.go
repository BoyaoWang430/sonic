//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"encoding/binary"
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

func (b *pathBlock) string(src string, begin int) (end, escape int, err types.ParsingError) {
	// A string needs only quotes and escapes. Reuse a classification
	// produced for whitespace, but do not build all three path masks for a key.
	if begin >= b.base && begin < b.end {
		return b.stringCached(src, begin)
	}
	return b.stringTail(src, begin)
}

// stringTail scans relaxed JSON string content after its opening quote.
// Bounds checks precede every load; integer addresses avoid exposing pointers
// beyond the allocation while crossing an escape or returning at EOF.
//
//go:nocheckptr
func (b *pathBlock) stringTail(src string, pos int) (end, escape int, err types.ParsingError) {
	base := uintptr(unsafe.Pointer(unsafe.StringData(src)))
	sp, se := base+uintptr(pos), base+uintptr(len(src))
	stop := min(se, sp+32)
	escape = -1
	for sp < stop && se-sp >= 8 {
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
		slash := x ^ ('\\' * tokenByteOnes)
		backslashes := (slash - tokenByteOnes) &^ slash & tokenByteHighBits
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		// Only the first match is used: subtraction may mark later bytes too.
		if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
			end = int(sp + uintptr(bits.TrailingZeros64(quotes)>>3) + 1 - base)
			runtime.KeepAlive(src)
			return end, escape, 0
		}
		if backslashes != 0 {
			sp += uintptr(bits.TrailingZeros64(backslashes) >> 3)
			goto scanEscape
		}
		sp += 8
	}
	if sp < stop {
		c := *(*byte)(unsafe.Pointer(sp))
		if c == '"' {
			end = int(sp + 1 - base)
			runtime.KeepAlive(src)
			return end, escape, 0
		}
		if c == '\\' {
			goto scanEscape
		}
		// Reuse the checked ordinary byte. The remaining one-byte suffix is
		// cheaper to inspect directly; longer tails can amortize classification.
		sp++
		if se-sp >= 2 && sp-base >= 8 {
			goto scanTail
		}
	}
scanBytes:
	for sp < stop {
		c := *(*byte)(unsafe.Pointer(sp))
		if c == '"' {
			end = int(sp + 1 - base)
			runtime.KeepAlive(src)
			return end, escape, 0
		}
		if c == '\\' {
			goto scanEscape
		}
		sp++
	}
	if se-sp == 1 && *(*byte)(unsafe.Pointer(sp)) == '\\' && escape < 0 {
		escape = int(sp - base)
	}
	if sp >= se || se-sp == 1 && *(*byte)(unsafe.Pointer(sp)) == '\\' {
		runtime.KeepAlive(src)
		return len(src), escape, types.ERR_EOF
	}
	{
		end, nextEscape, err, events, width := riscv.ScanPathStringBlock(src, int(sp-base))
		b.saveStringTail(end, events, width)
		if escape < 0 {
			escape = nextEscape
		}
		runtime.KeepAlive(src)
		return end, escape, err
	}

scanEscape:
	// Word and byte scans already located the slash; do not reload it.
	if escape < 0 {
		escape = int(sp - base)
	}
	if se-sp < 2 {
		runtime.KeepAlive(src)
		return len(src), escape, types.ERR_EOF
	}
	sp += 2
	goto scanBytes

scanTail:
	// Falling out of the word loop before stop leaves fewer than eight bytes.
	// Shift away the consumed prefix before classifying the bounded last word,
	// avoiding both repeated escapes and subtraction's cross-byte borrow flags.
	x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(se - 8))[:])
	x >>= (8 - (se - sp)) * 8
	slash := x ^ ('\\' * tokenByteOnes)
	backslashes := (slash - tokenByteOnes) &^ slash & tokenByteHighBits
	q := x ^ ('"' * tokenByteOnes)
	quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
	if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
		end = int(sp + uintptr(bits.TrailingZeros64(quotes)>>3) + 1 - base)
		runtime.KeepAlive(src)
		return end, escape, 0
	}
	if backslashes == 0 {
		runtime.KeepAlive(src)
		return len(src), escape, types.ERR_EOF
	}
	sp += uintptr(bits.TrailingZeros64(backslashes) >> 3)
	goto scanEscape
}
