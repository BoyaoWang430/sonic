//go:build sonic_compat || (!(amd64 && go1.20) && !(arm64 && go1.20) && !(riscv64 && go1.26 && riscv64.rva22u64) && !(sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package compat

import (
	"encoding/json"
	"errors"

	"github.com/bytedance/sonic/internal/native/types"
)

// FindTrailingNonSpace skips RFC 8259 whitespace starting at pos and returns
// (nextPos, true) if a trailing non-whitespace byte exists at buf[nextPos].
func FindTrailingNonSpace(buf string, pos int) (int, bool) {
	if pos < 0 {
		pos = 0
	}
	for pos < len(buf) && (uint64(types.SPACE_MASK)&(uint64(1)<<buf[pos])) != 0 {
		pos++
	}
	if pos >= len(buf) {
		return len(buf), false
	}
	return pos, true
}

// FindTrailingNonSpaceBytes skips RFC 8259 whitespace starting at pos in a byte slice.
func FindTrailingNonSpaceBytes(buf []byte, pos int) (int, bool) {
	if pos < 0 {
		pos = 0
	}
	for pos < len(buf) && (uint64(types.SPACE_MASK)&(uint64(1)<<buf[pos])) != 0 {
		pos++
	}
	if pos >= len(buf) {
		return len(buf), false
	}
	return pos, true
}

// NewTrailingSyntaxError constructs a populated *json.SyntaxError using a
// bounded <=7-byte stack probe so arbitrarily large trailing payloads are never
// copied, while preserving the active Go stdlib's Offset convention at pos.
func NewTrailingSyntaxError(firstByte byte, pos int) *json.SyntaxError {
	var probeBuf [7]byte
	probeBuf[0] = '0'
	probeBuf[1] = ' '
	n := 3
	switch firstByte {
	case '"':
		probeBuf[2], probeBuf[3] = '"', '"'
		n = 4
	case '{':
		probeBuf[2], probeBuf[3] = '{', '}'
		n = 4
	case '[':
		probeBuf[2], probeBuf[3] = '[', ']'
		n = 4
	case 't':
		copy(probeBuf[2:], "true")
		n = 6
	case 'f':
		copy(probeBuf[2:], "false")
		n = 7
	case 'n':
		copy(probeBuf[2:], "null")
		n = 6
	case '-':
		probeBuf[2], probeBuf[3] = '-', '0'
		n = 4
	default:
		probeBuf[2] = firstByte
	}

	var sink interface{}
	err := json.Unmarshal(probeBuf[:n], &sink)
	var se *json.SyntaxError
	if errors.As(err, &se) && se != nil {
		cp := *se
		delta := cp.Offset - 2
		if delta < 0 || delta > 1 {
			delta = stdTrailingOffsetDelta()
		}
		cp.Offset = int64(pos) + delta
		return &cp
	}
	return &json.SyntaxError{Offset: int64(pos) + stdTrailingOffsetDelta()}
}

func stdTrailingOffsetDelta() int64 {
	probe := [3]byte{'0', ' ', '0'}
	var sink interface{}
	err := json.Unmarshal(probe[:], &sink)
	var se *json.SyntaxError
	if errors.As(err, &se) && se != nil && se.Offset >= 2 {
		return se.Offset - 2
	}
	return 1
}
