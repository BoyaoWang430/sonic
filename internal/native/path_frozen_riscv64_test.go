//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"reflect"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

// frozenGetByPath finds the raw value at path and returns its start, updating p to its
// end. A non-nil state machine selects structural validation of the target;
// unrelated values are skipped with the native fast traversal policy.
func frozenGetByPath(s *string, p *int, path *[]interface{}, m *types.StateMachine) int {
	src := *s
	for _, component := range *path {
		ch := nextJSONByte(src, p)
		value := reflect.ValueOf(component)
		switch value.Kind() {
		case reflect.String:
			if ch != '{' {
				return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
			}
			key := value.String()
			for {
				ch = nextJSONByte(src, p)
				if ch == '}' {
					return frozenPathError(p, ch, types.ERR_NOT_FOUND)
				}
				if ch != '"' {
					return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
				}
				matched, err := frozenMatchPathKey(src, p, key)
				if err != 0 {
					return -int(err)
				}
				ch = nextJSONByte(src, p)
				if ch != ':' {
					return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
				}
				if matched {
					break
				}
				// Match the existing path traversal: skip unselected fields
				// quickly, then require a comma or the enclosing object end.
				SkipOneFast(s, p)
				ch = nextJSONByte(src, p)
				if ch == '}' {
					return frozenPathError(p, ch, types.ERR_NOT_FOUND)
				}
				if ch != ',' {
					return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
				}
			}
		case reflect.Int:
			if ch != '[' {
				return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
			}
			index := value.Int()
			if index < 0 {
				return frozenPathError(p, ch, types.ERR_UNSUPPORT_TYPE)
			}
			ch = nextJSONByte(src, p)
			if ch == ']' {
				return frozenPathError(p, ch, types.ERR_NOT_FOUND)
			}
			if ch == 0 {
				return -int(types.ERR_INVALID_CHAR)
			}
			*p--
			for ; index > 0; index-- {
				SkipOneFast(s, p)
				ch = nextJSONByte(src, p)
				if ch == ']' {
					return frozenPathError(p, ch, types.ERR_NOT_FOUND)
				}
				if ch != ',' {
					return frozenPathError(p, ch, types.ERR_INVALID_CHAR)
				}
			}
		default:
			return frozenPathError(p, ch, types.ERR_UNSUPPORT_TYPE)
		}
	}
	if m == nil {
		return SkipOneFast(s, p)
	}
	return SkipOne(s, p, m, 0)
}

func frozenPathError(p *int, ch byte, err types.ParsingError) int {
	// Ordinary syntax errors identify the consumed delimiter. At EOF preserve
	// the bounded end cursor rather than moving it before the missing byte.
	if ch != 0 && *p > 0 {
		*p--
	}
	return -int(err)
}

func frozenMatchPathKey(src string, p *int, key string) (bool, types.ParsingError) {
	begin := *p
	end, escape, _, err := riscv.ScanString(src, begin-1, false)
	*p = end
	if err != 0 {
		return false, err
	}
	contentEnd := end - 1
	if escape < 0 {
		return src[begin:contentEnd] == key, 0
	}
	var decoded [12]byte
	i, k := begin, 0
	for i < contentEnd && k < len(key) {
		if src[i] != '\\' {
			if src[i] != key[k] {
				return false, 0
			}
			i++
			k++
			continue
		}
		// Decode only the escape currently compared. Decoding the whole key
		// would incorrectly inspect bad escapes after the first mismatch.
		width := 2
		if i+1 < contentEnd && src[i+1] == 'u' {
			width = 6
			if i+8 <= contentEnd && (src[i+2] == 'd' || src[i+2] == 'D') &&
				(src[i+3] == '8' || src[i+3] == '9' || src[i+3] == 'a' || src[i+3] == 'A' || src[i+3] == 'b' || src[i+3] == 'B') &&
				src[i+6] == '\\' && src[i+7] == 'u' {
				width = 12
			}
		}
		if width > contentEnd-i {
			width = contentEnd - i
		}
		written, errorPos, err := riscv.Unquote(src[i:i+width], decoded[:width], 0)
		if err != 0 {
			// match_key's unescape helper reports the beginning of a bad hex
			// group or the end of the first surrogate, rather than Unquote's
			// individual offending byte. Keep that consumer-level convention.
			switch err {
			case types.ERR_INVALID_ESCAPE:
				*p = i + 1
			case types.ERR_INVALID_CHAR:
				*p = i + 2
				if errorPos >= 8 {
					*p = i + 8
				}
			case types.ERR_INVALID_UNICODE:
				*p = i + 6
				if width == 12 {
					*p = i + 8
				}
			default:
				*p = i + errorPos
			}
			if *p > contentEnd {
				*p = contentEnd
			}
			return false, err
		}
		if written > len(key)-k || string(decoded[:written]) != key[k:k+written] {
			return false, 0
		}
		i += width
		k += written
	}
	return i == contentEnd && k == len(key), 0
}
