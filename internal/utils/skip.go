package utils

import (
	"github.com/bytedance/sonic/internal/native/types"
)

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

func SkipNumber(src string, pos int) (ret int) {
	n := len(src)
	if pos >= n {
		return -int(types.ERR_EOF)
	}

	i := pos
	if src[i] == '-' {
		i++
	}
	ss := i

	var pointer bool
	var exponent bool
	var lastIsDigit bool
	var nextNeedDigit = true

	for ; i < n; i++ {
		c := src[i]
		if isDigit(c) {
			lastIsDigit = true
			nextNeedDigit = false
			continue
		} else if nextNeedDigit {
			return -int(types.ERR_INVALID_CHAR)
		} else if c == '.' {
			if !lastIsDigit || pointer || exponent || i == ss {
				return -int(types.ERR_INVALID_CHAR)
			}
			pointer = true
			lastIsDigit = false
			nextNeedDigit = true
			continue
		} else if c == 'e' || c == 'E' {
			if !lastIsDigit || exponent {
				return -int(types.ERR_INVALID_CHAR)
			}
			if i == n-1 {
				return -int(types.ERR_EOF)
			}
			exponent = true
			lastIsDigit = false
			nextNeedDigit = false
			continue
		} else if c == '-' || c == '+' {
			if prev := src[i-1]; prev != 'e' && prev != 'E' {
				return -int(types.ERR_INVALID_CHAR)
			}
			lastIsDigit = false
			nextNeedDigit = true
			continue
		} else {
			break
		}
	}

	if nextNeedDigit {
		return -int(types.ERR_EOF)
	}

	return i
}

// Hack: this is used for both checking space and cause friendly compile errors in 32-bit arch.
const _Sonic_Not_Support_32Bit_Arch__Checking_32Bit_Arch_Here = (1 << ' ') | (1 << '\t') | (1 << '\r') | (1 << '\n')

func IsSpace(c byte) bool {
	return (int(1<<c) & _Sonic_Not_Support_32Bit_Arch__Checking_32Bit_Arch_Here) != 0
}
