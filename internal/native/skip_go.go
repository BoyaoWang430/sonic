//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

/*
 * Copyright 2026 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package native

import (
	"sync"

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/kernels"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/utils"
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

var validateMachinePool = sync.Pool{
	New: func() interface{} {
		return &jsonscan.Machine{}
	},
}

// SkipOne dispatches between relaxed boundary location (SkipOneFast when
// B_NO_VALIDATE_JSON is set) and structural JSON validation (ValidateOne).
// Prefer calling ValidateOne or SkipOneFast directly rather than introducing
// new mixed-meaning flag bits.
func SkipOne(s *string, p *int, m *types.StateMachine, flags uint64) int {
	if flags&noValidateJSON != 0 {
		return SkipOneFast(s, p)
	}
	return ValidateOne(s, p, m, flags)
}

// ValidateOne validates a single JSON value prefix starting at *p via the
// shared jsonscan.Machine, updating *p to the end of the value (or error
// cursor) and publishing the active grammar stack into m on failure.
func ValidateOne(s *string, p *int, m *types.StateMachine, flags uint64) int {
	return ValidateOneAtDepth(s, p, m, flags, 0)
}

func ValidateOneAtDepth(s *string, p *int, m *types.StateMachine, flags uint64, baseDepth int) int {
	if baseDepth < 0 || baseDepth > types.MAX_RECURSE {
		if m != nil {
			m.Sp = 0
		}
		return -int(types.ERR_RECURSE_EXCEED_MAX)
	}
	if m != nil && baseDepth > len(m.Vt) {
		m.Sp = 0
		return -int(types.ERR_RECURSE_EXCEED_MAX)
	}
	if flags&noValidateJSON != 0 {
		return validateOneRelaxed(s, p, m)
	}

	src := *s
	if uint(*p) >= uint(len(src)) {
		*p = len(src)
		if m != nil {
			m.Sp = 0
			if len(m.Vt) > 0 {
				m.Vt[0] = skipValueState
				m.Sp = 1
			}
		}
		return -int(types.ERR_EOF)
	}

	maxDepth := jsonscan.StrictMaxContainerDepth
	if m != nil && len(m.Vt) < maxDepth {
		maxDepth = len(m.Vt)
	}
	validateStrings := flags&types.F_VALIDATE_STRING != 0

	jm := validateMachinePool.Get().(*jsonscan.Machine)
	jm.ResetValidateOne(validateStrings, baseDepth, maxDepth, *p)
	res := jm.Step(src, true)

	if res.Reason == jsonscan.StopDone {
		*p = res.Consumed
		if m != nil {
			m.Sp = 0
		}
		validateMachinePool.Put(jm)
		return res.ValueStart
	}

	*p = res.Consumed
	if m != nil {
		populateErrorStateMachine(m, &jm.Grammar, res.Code)
	}
	validateMachinePool.Put(jm)

	switch res.Code {
	case jsonscan.ErrEOF:
		return -int(types.ERR_EOF)
	case jsonscan.ErrDepthOverflow, jsonscan.ErrSpanOverflow:
		return -int(types.ERR_RECURSE_EXCEED_MAX)
	default:
		return -int(types.ERR_INVALID_CHAR)
	}
}

func validateOneState(s *string, p *int, m *types.StateMachine, flags uint64) int {
	return ValidateOneAtDepth(s, p, m, flags, 0)
}

func validateOneStateAtDepth(s *string, p *int, m *types.StateMachine, flags uint64, baseDepth int) int {
	return ValidateOneAtDepth(s, p, m, flags, baseDepth)
}

func populateErrorStateMachine(m *types.StateMachine, g *jsonscan.GrammarState, code jsonscan.ScanError) {
	depth := g.Depth
	if depth > len(m.Vt) {
		depth = len(m.Vt)
	}
	for i := 0; i < depth; i++ {
		isArr := (g.BitStack[uint(i)>>6] & (uint64(1) << (uint(i) & 63))) != 0
		if i == depth-1 {
			if g.Phase == jsonscan.PhaseArrayOpen {
				m.Vt[i] = skipFirstArrayState
				continue
			}
			if g.Phase == jsonscan.PhaseObjectOpen {
				m.Vt[i] = skipFirstObjectState
				continue
			}
		}
		if isArr {
			m.Vt[i] = skipArrayState
		} else {
			m.Vt[i] = skipObjectState
		}
	}
	m.Sp = depth
	if code == jsonscan.ErrDepthOverflow || code == jsonscan.ErrSpanOverflow || m.Sp >= len(m.Vt) {
		return
	}
	pending := -1
	switch g.Phase {
	case jsonscan.PhaseValue:
		if code == jsonscan.ErrEOF {
			pending = skipValueState
		}
	case jsonscan.PhaseObjectKey:
		pending = skipKeyState
	case jsonscan.PhaseObjectColon:
		pending = skipElementState
	case jsonscan.PhaseKeyString:
		if !g.WasFirstKey {
			pending = skipElementState
		}
	}
	if pending >= 0 {
		m.Vt[m.Sp] = pending
		m.Sp++
	}
}

// validateOneRelaxed preserves the explicit B_NO_VALIDATE_JSON contract when
// ValidateOne is called directly with noValidateJSON set (skipping containers
// and numbers via relaxed kernels while still checking top-level literals).
func validateOneRelaxed(s *string, p *int, m *types.StateMachine) int {
	src := *s
	pos := *p
	for uint(pos) < uint(len(src)) && utils.IsSpace(src[pos]) {
		pos++
	}
	if uint(pos) >= uint(len(src)) {
		*p = len(src)
		if m != nil {
			m.Sp = 0
			if len(m.Vt) > 0 {
				m.Vt[0] = skipValueState
				m.Sp = 1
			}
		}
		return -int(types.ERR_EOF)
	}
	ch := src[pos]
	pos++
	if ch == 0 {
		*p = pos
		if m != nil {
			m.Sp = 0
			if len(m.Vt) > 0 {
				m.Vt[0] = skipValueState
				m.Sp = 1
			}
		}
		return -int(types.ERR_EOF)
	}
	if m != nil {
		m.Sp = 0
	}
	start := pos - 1
	switch ch {
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		*p = kernels.SkipNumber(src, pos)
		return start
	case 'n', 't', 'f':
		literal, _ := jsonLiteral(ch)
		if err := consumeLiteral(src, start, &pos, literal); err != 0 {
			*p = pos
			return -int(err)
		}
		*p = pos
		return start
	case '[', '{':
		ret := skipContainerFast(src, &pos)
		*p = pos
		if ret < 0 {
			return ret
		}
		return start
	case '"':
		if err := skipStringToken(src, &pos, 0); err != 0 {
			*p = pos
			return -int(err)
		}
		*p = pos
		return start
	default:
		*p = pos
		return -int(types.ERR_INVALID_CHAR)
	}
}

// SkipOneFast only finds a value boundary. It intentionally does not validate
// number syntax, literal spelling, mixed container brackets, or string escapes.
func SkipOneFast(s *string, p *int) int {
	src := *s
	ch := pathOrdinaryByte(src, p)
	if ch == 0 {
		ch = nextJSONByteSlow(src, p)
	}
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
		*p = kernels.SkipNumber(src, *p)
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

func skipContainerFast(src string, p *int) int {
	start := *p - 1
	end, err := kernels.SkipContainer(src, start)
	*p = end
	if err != 0 {
		return -int(err)
	}
	return start
}
