//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"math"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

// Value identifies one token, leaving container contents to its caller. Like
// the other native backends, numeric fields and string offsets have different
// meanings depending on Vt; Dbuf and Dcap remain owned by the caller.
func Value(s unsafe.Pointer, n int, p int, v *types.JsonState, flags uint64) int {
	src := unsafe.String((*byte)(s), n)
	ch := nextJSONByte(src, &p)
	start := p - 1
	switch ch {
	case 0:
		v.Vt = types.V_EOF
	case '[':
		v.Vt = types.V_ARRAY
	case '{':
		v.Vt = types.V_OBJECT
	case ':', ',', ']', '}':
		if flags&types.F_ALLOW_CONTROL == 0 {
			v.Vt = -int64(types.ERR_INVALID_CHAR)
			return start
		}
		switch ch {
		case ':':
			v.Vt = 10
		case ',':
			v.Vt = 11
		case ']':
			v.Vt = 12
		case '}':
			v.Vt = 13
		}
	case 'n', 't', 'f':
		literal, kind := jsonLiteral(ch)
		if err := consumeLiteral(src, start, &p, literal); err != 0 {
			v.Vt = -int64(err)
		} else {
			v.Vt = kind
		}
	case '"':
		end, escape, _, err := riscv.ScanString(src, start, flags&types.F_VALIDATE_STRING != 0)
		if err != 0 {
			v.Vt = -int64(err)
			// vstring's interface reports the logical end for string errors.
			return len(src)
		}
		v.Vt, v.Iv, v.Ep = types.V_STRING, int64(start+1), escape
		p = end
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		v.Ep = start
		if flags&types.F_USE_NUMBER != 0 {
			end, err := riscv.ScanNumberStrict(src, start)
			v.Vt = types.V_DOUBLE
			if err != 0 {
				v.Vt = -int64(err)
			}
			return end
		}
		v.Iv, v.Dv = 0, 0
		value, end, err := number.ParsePrefix(src, start)
		if err != 0 {
			v.Vt, v.Dv = -int64(err), value.Float64
			return end
		}
		switch value.Kind {
		case number.Signed:
			v.Vt, v.Iv, v.Dv = types.V_INTEGER, value.Int64, float64(value.Int64)
		case number.Unsigned:
			v.Dv = float64(value.Uint64)
			if value.Uint64 <= math.MaxInt64 {
				v.Vt, v.Iv = types.V_INTEGER, int64(value.Uint64)
			} else {
				v.Vt = types.V_DOUBLE
			}
		case number.Float:
			v.Vt, v.Dv = types.V_DOUBLE, value.Float64
		}
		p = end
	default:
		v.Vt = -int64(types.ERR_INVALID_CHAR)
		return start
	}
	return p
}

// nextJSONByte consumes whitespace and the next byte. EOF cursors stay bounded
// by len(src), including the short whitespace tails of the old C advance_ns.
func nextJSONByte(src string, p *int) byte {
	i := *p
	if uint(i) < uint(len(src)) && src[i] > ' ' {
		*p = i + 1
		return src[i]
	}
	return nextJSONByteSlow(src, p)
}

func nextJSONByteSlow(src string, p *int) byte {
	if *p < 0 || *p >= len(src) {
		*p = len(src)
		return 0
	}
	i := riscv.SkipSpace(src, *p)
	*p = i
	if i == len(src) {
		return 0
	}
	*p = i + 1
	return src[i]
}

func jsonLiteral(ch byte) (string, types.ValueType) {
	switch ch {
	case 'n':
		return "null", types.V_NULL
	case 't':
		return "true", types.V_TRUE
	default:
		return "false", types.V_FALSE
	}
}

func consumeLiteral(src string, start int, p *int, literal string) types.ParsingError {
	if len(src)-start < len(literal) {
		*p = len(src)
		return types.ERR_EOF
	}
	for i := range literal {
		if src[start+i] != literal[i] {
			*p = start + i
			return types.ERR_INVALID_CHAR
		}
	}
	*p = start + len(literal)
	return 0
}
