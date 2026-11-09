// Package number implements JSON number syntax and conversion without depending
// on a decoder's node representation or a native instruction set.
package number

import "github.com/bytedance/sonic/internal/native/types"

// scanGeneric scans one JSON number prefix starting at start, without skipping space.
// On success, end is the first unread byte. In particular, "01" ends after "0",
// just as the native parser does; the caller must validate the following JSON
// delimiter. Other trailing bytes, such as the '+' in "1+2", also belong to the
// caller. Once a decimal point or exponent is encountered, its required digits
// must be present: "1." and "1e+" are errors, not successful integer prefixes.
//
// On error, end identifies the offending byte, or len(s) for ERR_EOF. A start
// outside [0, len(s)] returns ERR_INVALID_CHAR and the unchanged start.
func scanGeneric(s string, start int) (end int, err types.ParsingError) {
	if start < 0 || start > len(s) {
		return start, types.ERR_INVALID_CHAR
	}
	i := start
	if i == len(s) {
		return i, types.ERR_EOF
	}
	if s[i] == '-' {
		i++
		if i == len(s) {
			return i, types.ERR_EOF
		}
	}
	if !isDigit(s[i]) {
		return i, types.ERR_INVALID_CHAR
	}
	if s[i] == '0' {
		i++
	} else {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i < len(s) && s[i] == '.' {
		i++
		if i == len(s) {
			return i, types.ERR_EOF
		}
		if !isDigit(s[i]) {
			return i, types.ERR_INVALID_CHAR
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		if i == len(s) {
			return i, types.ERR_EOF
		}
		if !isDigit(s[i]) {
			return i, types.ERR_INVALID_CHAR
		}
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	}
	return i, 0
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
