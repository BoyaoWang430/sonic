//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package riscv

import "unsafe"

// ValidUTF8 reports whether src contains only valid UTF-8 encodings. It does not
// require padding. The selected assembly kernel validates ASCII and multibyte
// sequences within the logical input bounds.
func ValidUTF8(src string) bool {
	_, _, full := utf8Scan(unsafe.StringData(src), len(src), 0, nil, 0)
	return !full
}

// InvalidUTF8 records the absolute offsets of invalid bytes at or after start
// in positions, whose length limits the number of offsets written. Each invalid
// byte is consumed separately; valid multibyte sequences are consumed intact.
// Start must be in [0, len(src)] and identify the next unread byte.
//
// Next is the next unread offset and count is the number of offsets written.
// Full is true only when an invalid byte cannot be recorded: resume at next with
// a fresh output slice. Even when positions is empty, a valid prefix is consumed.
// Neither input nor output requires padding.
func InvalidUTF8(src string, start int, positions []int) (next, count int, full bool) {
	tail := src[start:]
	return utf8Scan(unsafe.StringData(tail), len(tail), start, unsafe.SliceData(positions), len(positions))
}

// utf8Scan processes src[:size], adding base to returned input offsets. It writes
// at most limit offsets to positions; a zero limit never dereferences positions.
//
//go:noescape
func utf8Scan(src *byte, size, base int, positions *int, limit int) (next, count int, full bool)
