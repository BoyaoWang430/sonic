//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"math/bits"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

const (
	skipValueState = iota
	skipArrayState
	skipObjectState
	skipKeyState
	skipElementState
	skipFirstArrayState
	skipFirstObjectState
)

const noValidateJSON = uint64(1 << types.B_NO_VALIDATE_JSON)

// SkipOne returns the first non-space position, or a negative ParsingError.
// The caller receives the end/error cursor through p. String validation is
// controlled by F_VALIDATE_STRING, independently of structural validation.
func SkipOne(s *string, p *int, m *types.StateMachine, flags uint64) int {
	if flags&noValidateJSON != 0 {
		return SkipOneFast(s, p)
	}
	return ValidateOne(s, p, m, flags)
}

// validateOneState retains the complete native grammar state, including error
// cursors, pending phases, and the bounded explicit stack.
func validateOneState(s *string, p *int, m *types.StateMachine, flags uint64) int {
	// Container frames survive between values. The key/colon/value phase is
	// temporary, so keep it separate instead of pushing and popping a stack
	// slot for each token. A return publishes the same logical grammar stack,
	// including the pending phase and its slot limit, as the native interface.
	pos, depth, state, pending := *p, 0, skipValueState, skipValueState
	src, start := *s, -1
	var block traversalBlock
	validateStrings := flags&types.F_VALIDATE_STRING != 0 && flags&noValidateJSON == 0
	var ch byte
	var resume, result int
	goto readValue

	// Non-space tokens go straight to their known grammar phase. Only whitespace
	// needs this shared scanner and its continuation dispatch.
space:
	for {
		// Successful token scans only advance pos; cached blocks are never revisited.
		if pos >= block.end {
			block.load(src, pos, validateStrings)
		}
		mask := block.nonspace >> uint(pos-block.base)
		if mask != 0 {
			pos += bits.TrailingZeros64(mask)
			ch = src[pos]
			break
		}
		pos = block.end
		if pos == len(src) {
			result = -int(types.ERR_EOF)
			goto finish
		}
	}
	pos++
	if ch == 0 {
		result = -int(types.ERR_EOF)
		goto finish
	}
	switch resume {
	case skipValueState:
		goto valueReady
	case skipKeyState:
		goto keyReady
	case skipElementState:
		goto colonReady
	case skipFirstArrayState:
		goto firstArrayReady
	case skipFirstObjectState:
		goto firstObjectReady
	default:
		goto containerReady
	}

readValue:
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		// A single space after a colon needs no mask lookup or phase dispatch.
		if ch == ' ' && pos+1 < len(src) && src[pos+1] > ' ' {
			ch = src[pos+1]
			pos += 2
			goto valueReady
		}
		resume = skipValueState
		goto space
	}
	pos++
valueReady:
	if start < 0 {
		start = pos - 1
	}
	pending = -1
	goto value

readKey:
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		resume = skipKeyState
		goto space
	}
	pos++
keyReady:
	if ch != '"' {
		result = -int(types.ERR_INVALID_CHAR)
		goto finish
	}
	pending = skipElementState
	if err := block.skipKey(src, &pos, validateStrings); err != 0 {
		result = -int(err)
		goto finish
	}

readColon:
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		resume = skipElementState
		goto space
	}
	pos++
colonReady:
	if ch != ':' {
		result = -int(types.ERR_INVALID_CHAR)
		goto finish
	}
	pending = skipValueState
	goto readValue

readFirstArray:
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		resume = skipFirstArrayState
		goto space
	}
	pos++
firstArrayReady:
	if ch == ']' {
		depth--
		if depth > 0 {
			state = m.Vt[depth-1]
		}
		goto afterValue
	}
	state = skipArrayState
	goto value

readFirstObject:
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		resume = skipFirstObjectState
		goto space
	}
	pos++
firstObjectReady:
	if ch == '}' {
		depth--
		if depth > 0 {
			state = m.Vt[depth-1]
		}
		goto afterValue
	}
	if ch != '"' {
		result = -int(types.ERR_INVALID_CHAR)
		goto finish
	}
	state = skipObjectState
	if err := block.skipKey(src, &pos, validateStrings); err != 0 {
		result = -int(err)
		goto finish
	}
	if depth == len(m.Vt) {
		result = -int(types.ERR_RECURSE_EXCEED_MAX)
		goto finish
	}
	pending = skipElementState
	goto readColon

value:
	switch ch {
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if flags&noValidateJSON != 0 {
			skipNumberFast(src, &pos)
		} else {
			end, err := riscv.ScanNumberStrict(src, pos-1)
			pos = end
			if err != 0 {
				// The native FSM reports truncated numeric candidates as invalid.
				if err == types.ERR_EOF {
					err = types.ERR_INVALID_CHAR
				}
				result = -int(err)
				goto finish
			}
		}
	case 'n', 't', 'f':
		// Dispatch already consumed the first character. Check fixed suffixes
		// on success; keep the original helper's exact error cursor on failure.
		if len(src)-pos >= 3 {
			if ch == 'n' && src[pos:pos+3] == "ull" || ch == 't' && src[pos:pos+3] == "rue" {
				pos += 3
				break
			}
			if ch == 'f' && len(src)-pos >= 4 && src[pos:pos+4] == "alse" {
				pos += 4
				break
			}
		}
		literal, _ := jsonLiteral(ch)
		if err := consumeLiteral(src, pos-1, &pos, literal); err != 0 {
			result = -int(err)
			goto finish
		}
	case '[', '{':
		if flags&noValidateJSON != 0 {
			if ret := skipContainerFast(src, &pos); ret < 0 {
				result = ret
				goto finish
			}
		} else {
			if depth == len(m.Vt) {
				result = -int(types.ERR_RECURSE_EXCEED_MAX)
				goto finish
			}
			if depth > 0 {
				m.Vt[depth-1] = state
			}
			depth++
			if ch == '[' {
				state = skipFirstArrayState
				goto readFirstArray
			}
			state = skipFirstObjectState
			goto readFirstObject
		}
	case '"':
		if err := block.skipString(src, &pos, validateStrings); err != 0 {
			result = -int(err)
			goto finish
		}
	default:
		result = -int(types.ERR_INVALID_CHAR)
		goto finish
	}

afterValue:
	if depth == 0 {
		result = start
		goto finish
	}
	if uint(pos) >= uint(len(src)) {
		goto eof
	}
	ch = src[pos]
	if ch <= ' ' {
		resume = skipArrayState
		goto space
	}
	pos++
containerReady:
	if state == skipArrayState {
		if ch == ']' {
			depth--
			if depth > 0 {
				state = m.Vt[depth-1]
			}
			goto afterValue
		}
		if ch != ',' {
			result = -int(types.ERR_INVALID_CHAR)
			goto finish
		}
		if depth == len(m.Vt) {
			result = -int(types.ERR_RECURSE_EXCEED_MAX)
			goto finish
		}
		pending = skipValueState
		goto readValue
	}
	if ch == '}' {
		depth--
		if depth > 0 {
			state = m.Vt[depth-1]
		}
		goto afterValue
	}
	if ch != ',' {
		result = -int(types.ERR_INVALID_CHAR)
		goto finish
	}
	if depth == len(m.Vt) {
		result = -int(types.ERR_RECURSE_EXCEED_MAX)
		goto finish
	}
	pending = skipKeyState
	goto readKey

eof:
	pos = len(src)
	result = -int(types.ERR_EOF)

finish:
	if depth > 0 {
		m.Vt[depth-1] = state
	}
	m.Sp = depth
	if pending >= 0 {
		m.Vt[m.Sp] = pending
		m.Sp++
	}
	*p = pos
	return result
}

// SkipOneFast only finds a value boundary. It intentionally does not validate
// number syntax, literal spelling, mixed container brackets, or string escapes.
func SkipOneFast(s *string, p *int) int {
	src := *s
	ch := nextJSONByte(src, p)
	start := *p - 1
	switch ch {
	case 0:
		return -int(types.ERR_EOF)
	case '[', '{':
		return skipContainerFast(src, p)
	case '"':
		if err := skipStringToken(src, p, 0); err != 0 {
			return -int(err)
		}
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		skipNumberFast(src, p)
	case 'n', 't', 'f':
		width := 4
		if ch == 'f' {
			width = 5
		}
		if len(src)-start < width {
			*p = len(src)
			return -int(types.ERR_EOF)
		}
		*p = start + width
	default:
		*p = start
		return -int(types.ERR_INVALID_CHAR)
	}
	return start
}

func skipNumberFast(src string, p *int) {
	*p = riscv.SkipNumber(src, *p)
}

func skipContainerFast(src string, p *int) int {
	start := *p - 1
	end, err := riscv.SkipContainer(src, start)
	*p = end
	if err != 0 {
		return -int(err)
	}
	return start
}
