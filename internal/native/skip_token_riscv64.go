//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"encoding/binary"
	"math/bits"
	"runtime"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/internal/utils"
)

// ValidateOne uses shared token traversal for scalar and RVV kernels.
// The state machine handles other modes, errors, and deeply nested values.
func ValidateOne(s *string, p *int, m *types.StateMachine, flags uint64) int {
	if flags != 0 {
		return validateOneState(s, p, m, flags)
	}
	src := *s
	if uint(*p) >= uint(len(src)) {
		return validateOneState(s, p, m, flags)
	}
	start := tokenSpace(src, *p)
	if start >= 0 {
		if end := tokenValue(src, start, 0); end >= 0 {
			*p, m.Sp = end, 0
			return start
		}
	}
	return validateOneState(s, p, m, flags)
}

// Bound Go recursion independently of the grammar's state-slot limit. Deeper
// inputs are scanned by the iterative state machine, not rejected.
const maxTokenDepth = 256

// tokenSpace requires 0 <= pos <= len(src). Only the four JSON whitespace
// bytes are discarded. Illegal control bytes reach the grammar and force the
// original state machine to produce the exact error.
func tokenSpace(src string, pos int) int {
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		return pos
	}
	return tokenSpaceSlow(src, pos)
}

// Short whitespace runs are scanned inline; longer runs use the native scanner.
//
//go:nocheckptr
func tokenSpaceSlow(src string, pos int) int {
	// Keep scan addresses as integers. A pointer at or past the end of the
	// allocation may be shared across calls after inlining and scanned by GC.
	base := uintptr((*rt.GoString)(unsafe.Pointer(&src)).Ptr)
	sp := base + uintptr(pos)
	se := base + uintptr(len(src))
	stop := min(se, sp+16)

	for sp < stop {
		if !utils.IsSpace(*(*byte)(unsafe.Pointer(sp))) {
			break
		}
		sp += 1
	}
	if sp >= se {
		runtime.KeepAlive(src)
		return -int(types.ERR_EOF)
	}
	ret := int(sp - base)
	if sp == stop {
		ret = riscv.SkipSpace(src, ret)
		if ret == len(src) {
			return -int(types.ERR_EOF)
		}
	}
	runtime.KeepAlive(src)
	return ret
}

// The caller has checked that pos identifies a byte, and removed whitespace.
func tokenValue(src string, pos, depth int) int {
	switch src[pos] {
	case '"':
		return tokenString(src, pos+1)
	case '{':
		if depth >= maxTokenDepth {
			return -1
		}
		return tokenObject(src, pos+1, depth+1)
	case '[':
		if depth >= maxTokenDepth {
			return -1
		}
		return tokenArray(src, pos+1, depth+1)
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		end, err := riscv.ScanNumberStrict(src, pos)
		if err == 0 {
			return end
		}
	case 'n':
		if len(src)-pos >= 4 && src[pos+1:pos+4] == "ull" {
			return pos + 4
		}
	case 't':
		if len(src)-pos >= 4 && src[pos+1:pos+4] == "rue" {
			return pos + 4
		}
	case 'f':
		if len(src)-pos >= 5 && src[pos+1:pos+5] == "alse" {
			return pos + 5
		}
	}
	return -1
}

func tokenObject(src string, pos, depth int) int {
	pos = tokenSpace(src, pos)
	if pos < 0 {
		return -1
	}
	if src[pos] == '}' {
		return pos + 1
	}
	for {
		if src[pos] != '"' {
			return -1
		}
		pos = tokenString(src, pos+1)
		if pos < 0 {
			return -1
		}
		pos = tokenSpace(src, pos)
		if pos < 0 || src[pos] != ':' {
			return -1
		}
		pos++
		// Consume the usual formatted-object separator without a slow-path call.
		if pos < len(src) && src[pos] == ' ' {
			pos++
		}
		pos = tokenSpace(src, pos)
		if pos < 0 {
			return -1
		}
		pos = tokenValue(src, pos, depth)
		if pos < 0 {
			return -1
		}
		pos = tokenSpace(src, pos)
		if pos < 0 {
			return -1
		}
		if src[pos] == '}' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -1
		}
		pos = tokenSpace(src, pos+1)
		if pos < 0 {
			return -1
		}
	}
}

func tokenArray(src string, pos, depth int) int {
	pos = tokenSpace(src, pos)
	if pos < 0 {
		return -1
	}
	if src[pos] == ']' {
		return pos + 1
	}
	for {
		pos = tokenValue(src, pos, depth)
		if pos < 0 {
			return -1
		}
		pos = tokenSpace(src, pos)
		if pos < 0 {
			return -1
		}
		if src[pos] == ']' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -1
		}
		pos = tokenSpace(src, pos+1)
		if pos < 0 {
			return -1
		}
	}
}

const tokenByteOnes uint64 = 0x0101010101010101
const tokenByteHighBits uint64 = 0x8080808080808080

// Probe short strings using the same word-at-a-time approach as the portable
// scanner. Escapes and longer strings continue in the native scanner without
// rescanning the completed prefix. Every word load stays within src.
//
//go:nocheckptr
func tokenString(src string, pos int) int {
	base := uintptr(unsafe.Pointer(unsafe.StringData(src)))
	sp, se := base+uintptr(pos), base+uintptr(len(src))
	stop := min(se, sp+32)
	for sp < stop && se-sp >= 8 {
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(sp))[:])
		b := x ^ ('\\' * tokenByteOnes)
		if (b-tokenByteOnes)&^b&tokenByteHighBits != 0 {
			goto scanNative
		}
		q := x ^ ('"' * tokenByteOnes)
		mask := (q - tokenByteOnes) &^ q & tokenByteHighBits
		if mask != 0 {
			end := int(sp + uintptr(bits.TrailingZeros64(mask)>>3) + 1 - base)
			runtime.KeepAlive(src)
			return end
		}
		sp += 8
	}
	if sp < stop && sp-base >= 8 {
		// A leading escape can continue directly in the native scanner.
		if *(*byte)(unsafe.Pointer(sp)) == '\\' {
			goto scanNative
		}
		// Falling out of the word loop before stop leaves fewer than eight bytes.
		// Keep the already scanned prefix available for a bounded last word.
		// Discard it before classification: SWAR subtraction may borrow into
		// later bytes, so masking its flags afterward would not be equivalent.
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Pointer(se - 8))[:])
		x >>= (8 - (se - sp)) * 8
		b := x ^ ('\\' * tokenByteOnes)
		backslashes := (b - tokenByteOnes) &^ b & tokenByteHighBits
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
			end := int(sp + uintptr(bits.TrailingZeros64(quotes)>>3) + 1 - base)
			runtime.KeepAlive(src)
			return end
		}
		if backslashes == 0 {
			runtime.KeepAlive(src)
			return -1
		}
		sp += uintptr(bits.TrailingZeros64(backslashes) >> 3)
	}
scanNative:
	pos = int(sp - base)
	end, err := riscv.SkipString(src[pos:], false)
	runtime.KeepAlive(src)
	if err != 0 {
		return -1
	}
	return pos + end
}
