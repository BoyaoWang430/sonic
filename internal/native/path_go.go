//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package native

import (
	"reflect"

	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
)

// GetByPath finds the raw value at path and returns its start, updating p to its
// end. A non-nil state machine selects structural validation of the target;
// unrelated values are skipped with the native fast traversal policy.
func GetByPath(s *string, p *int, path *[]interface{}, m *types.StateMachine) int {
	ret, end := getByPath(*s, *p, *path, m)
	*p = end
	return ret
}

// Keep traversal's cursor local across skipped values. The public wrapper
// publishes it once, including the precise cursor on failed path lookups.
func getByPath(src string, pos int, path []interface{}, m *types.StateMachine) (int, int) {
	var block pathBlock
	for _, component := range path {
		var ch byte
		if uint(pos) < uint(len(src)) && src[pos] > ' ' {
			ch = src[pos]
			pos++
		} else {
			ch, pos = block.nextSlow(src, pos)
		}
		var key string
		var index int64
		var isKey bool
		switch v := component.(type) {
		case string:
			key = v
			isKey = true
		case int:
			index = int64(v)
		default:
			value := reflect.ValueOf(component)
			switch value.Kind() {
			case reflect.String:
				key = value.String()
				isKey = true
			case reflect.Int:
				index = value.Int()
			default:
				return pathError(pos, ch, types.ERR_UNSUPPORT_TYPE)
			}
		}
		if isKey {
			if ch != '{' {
				return pathError(pos, ch, types.ERR_INVALID_CHAR)
			}
			for {
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					ch = src[pos]
					pos++
				} else {
					ch, pos = block.nextSlow(src, pos)
				}
				if ch == '}' {
					return pathError(pos, ch, types.ERR_NOT_FOUND)
				}
				if ch != '"' {
					return pathError(pos, ch, types.ERR_INVALID_CHAR)
				}
				var matched bool
				var err types.ParsingError
				pos, matched, err = block.matchKey(src, pos, key)
				if err != 0 {
					return -int(err), pos
				}
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					ch = src[pos]
					pos++
				} else {
					ch, pos = block.nextSlow(src, pos)
				}
				if ch != ':' {
					return pathError(pos, ch, types.ERR_INVALID_CHAR)
				}
				if matched {
					break
				}
				// Match the existing path traversal: skip unselected fields
				// quickly, then require a comma or the enclosing object end.
				pos, _ = block.skipValue(src, pos)
				if uint(pos) < uint(len(src)) && src[pos] > ' ' {
					ch = src[pos]
					pos++
				} else {
					ch, pos = block.nextSlow(src, pos)
				}
				if ch == '}' {
					return pathError(pos, ch, types.ERR_NOT_FOUND)
				}
				if ch != ',' {
					return pathError(pos, ch, types.ERR_INVALID_CHAR)
				}
			}
		} else {
			if ch != '[' {
				return pathError(pos, ch, types.ERR_INVALID_CHAR)
			}
			if index < 0 {
				return pathError(pos, ch, types.ERR_UNSUPPORT_TYPE)
			}
			var err int
			pos, err = block.arrayIndex(src, pos, index)
			if err != 0 {
				return err, pos
			}
		}
	}
	if m == nil {
		end, ret := block.skipValue(src, pos)
		return ret, end
	}
	// Only strings and whitespace can reuse the path's string classifications.
	// Other targets enter their validator without an extra token probe.
	if len(path) != 0 && uint(pos) < uint(len(src)) && (src[pos] == '"' || src[pos] <= ' ') {
		return block.validateTarget(src, pos, m)
	}
	p := pos
	ret := SkipOne(&src, &p, m, 0)
	return ret, p
}

// arrayIndex advances from an array's opening bracket to the requested value.
// Unselected values retain relaxed skipping; commas and the closing bracket
// are checked here before traversal returns to the next path component.
func (b *pathBlock) arrayIndex(src string, pos int, index int64) (int, int) {
	var ch byte
	if uint(pos) < uint(len(src)) && src[pos] > ' ' {
		ch = src[pos]
		pos++
	} else {
		ch, pos = b.nextSlow(src, pos)
	}
	if ch == ']' {
		ret, end := pathError(pos, ch, types.ERR_NOT_FOUND)
		return end, ret
	}
	if ch == 0 {
		return pos, -int(types.ERR_INVALID_CHAR)
	}
	pos--
	for ; index > 0; index-- {
		pos, _ = b.skipValue(src, pos)
		if uint(pos) < uint(len(src)) && src[pos] > ' ' {
			ch = src[pos]
			pos++
		} else {
			ch, pos = b.nextSlow(src, pos)
		}
		if ch == ']' {
			ret, end := pathError(pos, ch, types.ERR_NOT_FOUND)
			return end, ret
		}
		if ch != ',' {
			ret, end := pathError(pos, ch, types.ERR_INVALID_CHAR)
			return end, ret
		}
	}
	return pos, 0
}

func pathError(pos int, ch byte, err types.ParsingError) (int, int) {
	// Ordinary syntax errors identify the consumed delimiter. At EOF preserve
	// the bounded end cursor rather than moving it before the missing byte.
	if ch != 0 && pos > 0 {
		pos--
	}
	return -int(err), pos
}

// matchEscapedPathKey compares only the escapes reached before a mismatch.
// The complete string boundary was already checked by the path block scanner.
func matchEscapedPathKey(src string, key string, begin, end int) (int, bool, types.ParsingError) {
	contentEnd := end - 1
	var decoded [12]byte
	i, k := begin, 0
	for i < contentEnd && k < len(key) {
		if src[i] != '\\' {
			if src[i] != key[k] {
				return end, false, 0
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
		written, errorPos, err := kernels.Unquote(src[i:i+width], decoded[:width], 0)
		if err != 0 {
			var errPos int
			switch err {
			case types.ERR_INVALID_ESCAPE:
				errPos = i + 1
			case types.ERR_INVALID_CHAR:
				errPos = i + 2
				if errorPos >= 8 {
					errPos = i + 8
				}
			case types.ERR_INVALID_UNICODE:
				errPos = i + 6
				if width == 12 {
					errPos = i + 8
				}
			default:
				errPos = i + errorPos
			}
			if errPos > contentEnd {
				errPos = contentEnd
			}
			return errPos, false, err
		}
		if written > len(key)-k || string(decoded[:written]) != key[k:k+written] {
			return end, false, 0
		}
		i += width
		k += written
	}
	return end, i == contentEnd && k == len(key), 0
}
