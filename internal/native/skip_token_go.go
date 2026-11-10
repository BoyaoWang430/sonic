//go:build (amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"encoding/binary"
	"math/bits"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/internal/utils"
)

// ValidateOne uses shared token traversal across the architecture kernels.
// The state machine handles other modes, errors, and deeply nested values.
func ValidateOne(s *string, p *int, m *types.StateMachine, flags uint64) int {
	return ValidateOneAtDepth(s, p, m, flags, 0)
}

func ValidateOneAtDepth(s *string, p *int, m *types.StateMachine, flags uint64, baseDepth int) int {
	if flags != 0 || baseDepth >= maxTokenDepth {
		return validateOneStateAtDepth(s, p, m, flags, baseDepth)
	}
	src := *s
	if uint(*p) >= uint(len(src)) {
		return validateOneStateAtDepth(s, p, m, flags, baseDepth)
	}
	start := tokenSpace(src, *p)
	if start >= 0 {
		if end := tokenValue(src, start, baseDepth); end >= 0 {
			*p = end
			if m != nil {
				m.Sp = 0
			}
			return start
		}
	}
	return validateOneStateAtDepth(s, p, m, flags, baseDepth)
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
func tokenSpaceSlow(src string, pos int) int {
	se := len(src)
	if pos >= se {
		return -int(types.ERR_EOF)
	}
	if !utils.IsSpace(src[pos]) {
		return pos
	}
	base := unsafe.Pointer(unsafe.StringData(src))
	sp := pos + 1
	stop := minNative(se, sp+24)
	for sp < stop && se-sp >= 8 {
		d := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, sp))[:]) ^ 0x2020202020202020
		if d == 0 {
			sp += 8
			continue
		}
		sp += bits.TrailingZeros64(d) >> 3
		if src[sp] > ' ' {
			return sp
		}
		break
	}
	for sp < stop {
		if !utils.IsSpace(src[sp]) {
			break
		}
		sp++
	}
	if sp >= se {
		return -int(types.ERR_EOF)
	}
	ret := sp
	if sp == stop {
		ret = kernels.SkipSpace(src, ret)
		if ret == len(src) {
			return -int(types.ERR_EOF)
		}
	}
	return ret
}

// The caller has checked that pos identifies a byte, and removed whitespace.
func tokenValue(src string, pos, depth int) int {
	switch ch := src[pos]; ch {
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
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		p := pos + 1
		limit := p + 18
		if limit > len(src) {
			limit = len(src)
		}
		for p < limit && src[p]-'0' <= 9 {
			p++
		}
		if p < len(src) {
			switch src[p] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				return p
			}
		}
		end, err := kernels.ScanNumberStrict(src, pos)
		if err == 0 {
			return end
		}
	case '0':
		if pos+1 < len(src) {
			switch src[pos+1] {
			case ',', '}', ']', ' ', '\n', '\r', '\t':
				return pos + 1
			}
		}
		end, err := kernels.ScanNumberStrict(src, pos)
		if err == 0 {
			return end
		}
	case '-':
		end, err := kernels.ScanNumberStrict(src, pos)
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
		if pos+3 < len(src) {
			c0 := src[pos+1]
			if c0 != '"' && c0 != '\\' {
				if src[pos+2] == '"' && src[pos+3] == ':' {
					pos += 4
					goto afterColon
				}
				if pos+4 < len(src) {
					c1 := src[pos+2]
					if c1 != '"' && c1 != '\\' && src[pos+3] == '"' && src[pos+4] == ':' {
						pos += 5
						goto afterColon
					}
				}
			}
		}
		pos = tokenString(src, pos+1)
		if pos < 0 {
			return -1
		}
		if uint(pos) >= uint(len(src)) || src[pos] != ':' {
			pos = tokenSpace(src, pos)
			if pos < 0 || src[pos] != ':' {
				return -1
			}
		}
		pos++
	afterColon:
		// Consume the usual formatted-object separator without a slow-path call.
		if uint(pos) < uint(len(src)) && src[pos] == ' ' {
			pos++
		}
		if uint(pos) >= uint(len(src)) || src[pos] <= ' ' {
			pos = tokenSpaceSlow(src, pos)
			if pos < 0 {
				return -1
			}
		}
		switch src[pos] {
		case '"':
			pos = tokenString(src, pos+1)
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			p := pos + 1
			limit := p + 18
			if limit > len(src) {
				limit = len(src)
			}
			for p < limit && src[p]-'0' <= 9 {
				p++
			}
			if p < len(src) {
				switch src[p] {
				case ',', '}':
					pos = p
					goto afterObjValFast
				case ' ', '\n', '\r', '\t':
					pos = p
					goto afterObjVal
				}
			}
			end, err := kernels.ScanNumberStrict(src, pos)
			if err != 0 {
				return -1
			}
			pos = end
		case '0':
			if pos+1 < len(src) {
				switch src[pos+1] {
				case ',', '}':
					pos++
					goto afterObjValFast
				case ' ', '\n', '\r', '\t':
					pos++
					goto afterObjVal
				}
			}
			end, err := kernels.ScanNumberStrict(src, pos)
			if err != 0 {
				return -1
			}
			pos = end
		case 't':
			if len(src)-pos >= 4 && src[pos+1:pos+4] == "rue" {
				pos += 4
			} else {
				return -1
			}
		case 'f':
			if len(src)-pos >= 5 && src[pos+1:pos+5] == "alse" {
				pos += 5
			} else {
				return -1
			}
		case 'n':
			if len(src)-pos >= 4 && src[pos+1:pos+4] == "ull" {
				pos += 4
			} else {
				return -1
			}
		default:
			pos = tokenValue(src, pos, depth)
		}
		if pos < 0 {
			return -1
		}
	afterObjVal:
		if uint(pos) >= uint(len(src)) || src[pos] <= ' ' {
			pos = tokenSpaceSlow(src, pos)
			if pos < 0 {
				return -1
			}
		}
	afterObjValFast:
		if src[pos] == '}' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -1
		}
		pos++
		if uint(pos) < uint(len(src)) {
			c := src[pos]
			if c == '"' {
				continue
			}
			if c == '\n' || c == ' ' || c == '\t' || c == '\r' {
				pos++
				for uint(pos) < uint(len(src)) && (src[pos] == ' ' || src[pos] == '\t') {
					pos++
				}
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					continue
				}
			}
		}
		pos = tokenSpace(src, pos)
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
		switch src[pos] {
		case '"':
			pos = tokenString(src, pos+1)
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			p := pos + 1
			limit := p + 18
			if limit > len(src) {
				limit = len(src)
			}
			for p < limit && src[p]-'0' <= 9 {
				p++
			}
			if p < len(src) {
				switch src[p] {
				case ',', ']':
					pos = p
					goto afterArrValFast
				case ' ', '\n', '\r', '\t':
					pos = p
					goto afterArrVal
				}
			}
			end, err := kernels.ScanNumberStrict(src, pos)
			if err != 0 {
				return -1
			}
			pos = end
		case '0':
			if pos+1 < len(src) {
				switch src[pos+1] {
				case ',', ']':
					pos++
					goto afterArrValFast
				case ' ', '\n', '\r', '\t':
					pos++
					goto afterArrVal
				}
			}
			end, err := kernels.ScanNumberStrict(src, pos)
			if err != 0 {
				return -1
			}
			pos = end
		case 't':
			if len(src)-pos >= 4 && src[pos+1:pos+4] == "rue" {
				pos += 4
			} else {
				return -1
			}
		case 'f':
			if len(src)-pos >= 5 && src[pos+1:pos+5] == "alse" {
				pos += 5
			} else {
				return -1
			}
		case 'n':
			if len(src)-pos >= 4 && src[pos+1:pos+4] == "ull" {
				pos += 4
			} else {
				return -1
			}
		default:
			pos = tokenValue(src, pos, depth)
		}
		if pos < 0 {
			return -1
		}
	afterArrVal:
		if uint(pos) >= uint(len(src)) || src[pos] <= ' ' {
			pos = tokenSpaceSlow(src, pos)
			if pos < 0 {
				return -1
			}
		}
	afterArrValFast:
		if src[pos] == ']' {
			return pos + 1
		}
		if src[pos] != ',' {
			return -1
		}
		pos++
		if uint(pos) < uint(len(src)) {
			c := src[pos]
			if c > ' ' {
				continue
			}
			if c == '\n' || c == ' ' || c == '\t' || c == '\r' {
				pos++
				for uint(pos) < uint(len(src)) && (src[pos] == ' ' || src[pos] == '\t') {
					pos++
				}
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					continue
				}
			}
		}
		pos = tokenSpace(src, pos)
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
func tokenString(src string, pos int) int {
	base := unsafe.Pointer(rt.StringData(src))
	sp, se := pos, len(src)
	stop := minNative(se, sp+32)
	for sp < stop && se-sp >= 8 {
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, sp))[:])
		b := x ^ ('\\' * tokenByteOnes)
		if (b-tokenByteOnes)&^b&tokenByteHighBits != 0 {
			goto scanNative
		}
		q := x ^ ('"' * tokenByteOnes)
		mask := (q - tokenByteOnes) &^ q & tokenByteHighBits
		if mask != 0 {
			return sp + (bits.TrailingZeros64(mask) >> 3) + 1
		}
		sp += 8
	}
	if sp < stop && sp >= 8 {
		// A leading escape can continue directly in the native scanner.
		if src[sp] == '\\' {
			goto scanNative
		}
		// Falling out of the word loop before stop leaves fewer than eight bytes.
		// Keep the already scanned prefix available for a bounded last word.
		// Discard it before classification: SWAR subtraction may borrow into
		// later bytes, so masking its flags afterward would not be equivalent.
		x := binary.LittleEndian.Uint64((*[8]byte)(unsafe.Add(base, se-8))[:])
		x >>= (8 - (se - sp)) * 8
		b := x ^ ('\\' * tokenByteOnes)
		backslashes := (b - tokenByteOnes) &^ b & tokenByteHighBits
		q := x ^ ('"' * tokenByteOnes)
		quotes := (q - tokenByteOnes) &^ q & tokenByteHighBits
		if quotes != 0 && (backslashes == 0 || bits.TrailingZeros64(quotes) < bits.TrailingZeros64(backslashes)) {
			return sp + (bits.TrailingZeros64(quotes) >> 3) + 1
		}
		if backslashes == 0 {
			return -1
		}
		sp += bits.TrailingZeros64(backslashes) >> 3
	}
scanNative:
	pos = sp
	end, err := kernels.SkipStringRelaxed(src[pos:])
	if err != 0 {
		return -1
	}
	return pos + end
}
