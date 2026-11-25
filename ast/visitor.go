/*
 * Copyright 2021 ByteDance Inc.
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

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

// Visitor handles the callbacks during preorder traversal of a JSON AST.
//
// According to the JSON RFC8259, a JSON AST can be defined by
// the following rules without separator / whitespace tokens.
//
//	JSON-AST  = value
//	value     = false / null / true / object / array / number / string
//	object    = begin-object [ member *( member ) ] end-object
//	member    = string value
//	array     = begin-array [ value *( value ) ] end-array
type Visitor interface {

	// OnNull handles a JSON null value.
	OnNull() error

	// OnBool handles a JSON true / false value.
	OnBool(v bool) error

	// OnString handles a JSON string value.
	OnString(v string) error

	// OnInt64 handles a JSON number value with int64 type.
	OnInt64(v int64, n json.Number) error

	// OnFloat64 handles a JSON number value with float64 type.
	OnFloat64(v float64, n json.Number) error

	// OnObjectBegin handles the beginning of a JSON object value with a
	// suggested capacity that can be used to make your custom object container.
	//
	// After this point the visitor will receive a sequence of callbacks like
	// [string, value, string, value, ......, ObjectEnd].
	//
	// Note:
	// 1. This is a recursive definition which means the value can
	// also be a JSON object / array described by a sequence of callbacks.
	// 2. The suggested capacity will be 0 if current object is empty.
	// 3. Currently sonic use a fixed capacity for non-empty object (keep in
	// sync with ast.Node) which might not be very suitable. This may be
	// improved in future version.
	OnObjectBegin(capacity int) error

	// OnObjectKey handles a JSON object key string in member.
	OnObjectKey(key string) error

	// OnObjectEnd handles the ending of a JSON object value.
	OnObjectEnd() error

	// OnArrayBegin handles the beginning of a JSON array value with a
	// suggested capacity that can be used to make your custom array container.
	//
	// After this point the visitor will receive a sequence of callbacks like
	// [value, value, value, ......, ArrayEnd].
	//
	// Note:
	// 1. This is a recursive definition which means the value can
	// also be a JSON object / array described by a sequence of callbacks.
	// 2. The suggested capacity will be 0 if current array is empty.
	// 3. Currently sonic use a fixed capacity for non-empty array (keep in
	// sync with ast.Node) which might not be very suitable. This may be
	// improved in future version.
	OnArrayBegin(capacity int) error

	// OnArrayEnd handles the ending of a JSON array value.
	OnArrayEnd() error
}

// VisitorOptions contains all Visitor's options. The default value is an
// empty VisitorOptions{}.
type VisitorOptions struct {
	// OnlyNumber indicates parser to directly return number value without
	// conversion, then the first argument of OnInt64 / OnFloat64 will always
	// be zero.
	OnlyNumber bool
}

var defaultVisitorOptions = &VisitorOptions{}

// Preorder decodes one JSON value prefix and callbacks each AST event to visitor
// during preorder traversal via shared jsonscan.Machine ModeEventSink.
// Any visitor method returning an error breaks the traversal and returns that error.
func Preorder(str string, visitor Visitor, opts *VisitorOptions) error {
	if opts == nil {
		opts = defaultVisitorOptions
	}
	onlyNumber := opts.OnlyNumber

	m := acquireASTScanMachine()
	m.Reset(jsonscan.ASTEventProfile(onlyNumber), jsonscan.ModeEventSink)
	sink := astVisitorSink{
		visitor:    visitor,
		onlyNumber: onlyNumber,
	}
	m.ConfigureEventSink(&sink)
	res := m.Step(str, true)
	switch res.Reason {
	case jsonscan.StopDone:
		releaseASTScanMachine(m)
		return nil
	case jsonscan.StopUserAbort:
		uerr := m.SinkErr()
		numErr := sink.numParseErr
		releaseASTScanMachine(m)
		if numErr != 0 {
			return numErr
		}
		return uerr
	default:
		releaseASTScanMachine(m)
		return mapScanErrorToAST(res.Code)
	}
}

type astVisitorSink struct {
	visitor     Visitor
	onlyNumber  bool
	numParseErr types.ParsingError
}

func (s *astVisitorSink) OnNull() error {
	return s.visitor.OnNull()
}

func (s *astVisitorSink) OnBool(v bool) error {
	return s.visitor.OnBool(v)
}

func (s *astVisitorSink) OnString(v string) error {
	return s.visitor.OnString(v)
}

func (s *astVisitorSink) OnNumber(rawSpan string, lexRes jsonscan.LexNumberResult) error {
	jn := json.Number(rawSpan)
	if s.onlyNumber {
		return s.visitor.OnFloat64(0, jn)
	}
	switch lexRes.HintKind {
	case jsonscan.NumberHintUint:
		if lexRes.HintBits <= math.MaxInt64 {
			return s.visitor.OnInt64(int64(lexRes.HintBits), jn)
		}
		return s.visitor.OnFloat64(float64(lexRes.HintBits), jn)
	case jsonscan.NumberHintSint:
		return s.visitor.OnInt64(int64(lexRes.HintBits), jn)
	case jsonscan.NumberHintFloat:
		return s.visitor.OnFloat64(math.Float64frombits(lexRes.HintBits), jn)
	}
	resNum, perr := number.ParseValidatedNumber(rawSpan)
	if perr != 0 {
		s.numParseErr = perr
		return errASTVisitorNumFault
	}
	switch resNum.Kind {
	case number.Signed:
		return s.visitor.OnInt64(resNum.Int64, jn)
	case number.Unsigned:
		if resNum.Uint64 <= math.MaxInt64 {
			return s.visitor.OnInt64(int64(resNum.Uint64), jn)
		}
		return s.visitor.OnFloat64(float64(resNum.Uint64), jn)
	default:
		return s.visitor.OnFloat64(resNum.Float64, jn)
	}
}

func (s *astVisitorSink) OnObjectBegin() (jsonscan.EventContainerAction, error) {
	if err := s.visitor.OnObjectBegin(_DEFAULT_NODE_CAP); err != nil {
		if err == VisitOPSkip {
			return jsonscan.EventContainerSkip, nil
		}
		return jsonscan.EventContainerContinue, err
	}
	return jsonscan.EventContainerContinue, nil
}

func (s *astVisitorSink) OnObjectKey(key string) error {
	return s.visitor.OnObjectKey(key)
}

func (s *astVisitorSink) OnObjectEnd() error {
	return s.visitor.OnObjectEnd()
}

func (s *astVisitorSink) OnArrayBegin() (jsonscan.EventContainerAction, error) {
	if err := s.visitor.OnArrayBegin(_DEFAULT_NODE_CAP); err != nil {
		if err == VisitOPSkip {
			return jsonscan.EventContainerSkip, nil
		}
		return jsonscan.EventContainerContinue, err
	}
	return jsonscan.EventContainerContinue, nil
}

func (s *astVisitorSink) OnArrayEnd() error {
	return s.visitor.OnArrayEnd()
}

// If visitor return this error on `OnObjectBegin()` or `OnArrayBegin()`,
// the traverser will skip entry object or array
var VisitOPSkip = errors.New("")
