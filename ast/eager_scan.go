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

package ast

import (
	"encoding/json"
	"errors"
	"math"
	"sync"

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

const maxPooledASTUnescCap = 4096

var (
	errASTSinkTypeMismatch = errors.New("ast: unexpected root container kind")
	errASTVisitorNumFault  = errors.New("ast: number conversion fault")

	astScanMachinePool = sync.Pool{
		New: func() interface{} {
			return &jsonscan.Machine{}
		},
	}
)

func acquireASTScanMachine() *jsonscan.Machine {
	return astScanMachinePool.Get().(*jsonscan.Machine)
}

func releaseASTScanMachine(m *jsonscan.Machine) {
	unesc := m.Unesc
	m.ReleasePoolRefs()
	if cap(unesc) <= maxPooledASTUnescCap {
		m.Unesc = unesc[:0]
	}
	astScanMachinePool.Put(m)
}

func mapScanErrorToAST(code jsonscan.ScanError) types.ParsingError {
	switch code {
	case jsonscan.ErrEOF:
		return types.ERR_EOF
	case jsonscan.ErrInvalidEscape:
		return types.ERR_INVALID_ESCAPE
	case jsonscan.ErrFloatInf:
		return types.ERR_FLOAT_INFINITY
	case jsonscan.ErrDepthOverflow, jsonscan.ErrSpanOverflow:
		return types.ERR_RECURSE_EXCEED_MAX
	default:
		return types.ERR_INVALID_CHAR
	}
}

type eagerFrameKind uint8

const (
	eagerFrameObject eagerFrameKind = iota + 1
	eagerFrameArray
)

type eagerContainerFrame struct {
	kind eagerFrameKind
	obj  map[string]interface{}
	arr  []interface{}
	key  string
}

// astInterfaceSink materializes Go interface{} values from jsonscan.Machine's
// ModeEventSink without duplicating JSON container grammar or building an
// intermediate document-wide OptDec tape.
type astInterfaceSink struct {
	useNumber        bool
	expectRoot       byte
	rootTypeMismatch bool
	rootVal          interface{}
	sp               int
	inline           [32]eagerContainerFrame
	ext              []eagerContainerFrame
}

func (s *astInterfaceSink) frameAt(idx int) *eagerContainerFrame {
	if idx < len(s.inline) {
		return &s.inline[idx]
	}
	return &s.ext[idx-len(s.inline)]
}

func (s *astInterfaceSink) pushFrame(kind eagerFrameKind) {
	if s.sp < len(s.inline) {
		s.inline[s.sp] = eagerContainerFrame{kind: kind}
	} else {
		extIdx := s.sp - len(s.inline)
		if extIdx < len(s.ext) {
			s.ext[extIdx] = eagerContainerFrame{kind: kind}
		} else {
			s.ext = append(s.ext, eagerContainerFrame{kind: kind})
		}
	}
	s.sp++
}

func (s *astInterfaceSink) emitValue(v interface{}) error {
	if s.sp == 0 {
		s.rootVal = v
		return nil
	}
	top := s.frameAt(s.sp - 1)
	if top.kind == eagerFrameArray {
		if top.arr == nil {
			top.arr = make([]interface{}, 0, 4)
		}
		top.arr = append(top.arr, v)
		return nil
	}
	if top.obj == nil {
		top.obj = make(map[string]interface{}, 8)
	}
	top.obj[top.key] = v
	return nil
}

func (s *astInterfaceSink) OnNull() error {
	if s.sp == 0 && s.expectRoot != 0 {
		s.rootTypeMismatch = true
		return errASTSinkTypeMismatch
	}
	return s.emitValue(nil)
}

func (s *astInterfaceSink) OnBool(v bool) error {
	if s.sp == 0 && s.expectRoot != 0 {
		s.rootTypeMismatch = true
		return errASTSinkTypeMismatch
	}
	return s.emitValue(v)
}

func (s *astInterfaceSink) OnString(v string) error {
	if s.sp == 0 && s.expectRoot != 0 {
		s.rootTypeMismatch = true
		return errASTSinkTypeMismatch
	}
	return s.emitValue(v)
}

func (s *astInterfaceSink) OnNumber(rawSpan string, lexRes jsonscan.LexNumberResult) error {
	if s.sp == 0 && s.expectRoot != 0 {
		s.rootTypeMismatch = true
		return errASTSinkTypeMismatch
	}
	if s.useNumber {
		return s.emitValue(json.Number(rawSpan))
	}
	switch lexRes.HintKind {
	case jsonscan.NumberHintUint:
		return s.emitValue(float64(lexRes.HintBits))
	case jsonscan.NumberHintSint:
		if lexRes.NegZero {
			return s.emitValue(math.Copysign(0, -1))
		}
		return s.emitValue(float64(int64(lexRes.HintBits)))
	case jsonscan.NumberHintFloat:
		return s.emitValue(math.Float64frombits(lexRes.HintBits))
	}
	f, err := number.ParseGoFloat64(rawSpan)
	if err != nil {
		return err
	}
	return s.emitValue(f)
}

func (s *astInterfaceSink) OnObjectBegin() (jsonscan.EventContainerAction, error) {
	if s.sp == 0 && s.expectRoot != 0 && s.expectRoot != '{' {
		s.rootTypeMismatch = true
		return jsonscan.EventContainerContinue, errASTSinkTypeMismatch
	}
	s.pushFrame(eagerFrameObject)
	return jsonscan.EventContainerContinue, nil
}

func (s *astInterfaceSink) OnObjectKey(key string) error {
	s.frameAt(s.sp - 1).key = key
	return nil
}

func (s *astInterfaceSink) OnObjectEnd() error {
	s.sp--
	f := s.frameAt(s.sp)
	m := f.obj
	f.obj = nil
	f.key = ""
	if m == nil {
		m = map[string]interface{}{}
	}
	return s.emitValue(m)
}

func (s *astInterfaceSink) OnArrayBegin() (jsonscan.EventContainerAction, error) {
	if s.sp == 0 && s.expectRoot != 0 && s.expectRoot != '[' {
		s.rootTypeMismatch = true
		return jsonscan.EventContainerContinue, errASTSinkTypeMismatch
	}
	s.pushFrame(eagerFrameArray)
	return jsonscan.EventContainerContinue, nil
}

func (s *astInterfaceSink) OnArrayEnd() error {
	s.sp--
	f := s.frameAt(s.sp)
	a := f.arr
	f.arr = nil
	if a == nil {
		a = []interface{}{}
	}
	return s.emitValue(a)
}

func parseEagerInterface(src string, useNumber bool, expectRoot byte) (consumed int, val interface{}, errPos int, perr types.ParsingError, numErr error) {
	m := acquireASTScanMachine()
	m.Reset(jsonscan.ASTEventProfile(useNumber), jsonscan.ModeEventSink)
	var sink astInterfaceSink
	sink.useNumber = useNumber
	sink.expectRoot = expectRoot
	m.ConfigureEventSink(&sink)
	res := m.Step(src, true)
	switch res.Reason {
	case jsonscan.StopDone:
		v := sink.rootVal
		releaseASTScanMachine(m)
		return res.Consumed, v, 0, 0, nil
	case jsonscan.StopUserAbort:
		uerr := m.SinkErr()
		mismatch := sink.rootTypeMismatch
		releaseASTScanMachine(m)
		if mismatch {
			pos := res.ValueStart
			if pos < 0 {
				pos = 0
			}
			return 0, nil, pos, types.ERR_INVALID_CHAR, nil
		}
		return 0, nil, res.Consumed, 0, uerr
	default:
		releaseASTScanMachine(m)
		return 0, nil, res.ErrorOffset, mapScanErrorToAST(res.Code), nil
	}
}
