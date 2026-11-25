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

package jsonscan

import "github.com/bytedance/sonic/internal/native/types"

// EventContainerAction instructs the shared grammar driver how to proceed after
// opening an object or array in ModeEventSink.
type EventContainerAction uint8

const (
	EventContainerContinue EventContainerAction = iota
	EventContainerSkip
)

// EventSink consumes preorder JSON structural and scalar events emitted by
// Machine's single container grammar without building an intermediate tape
// or coupling internal/jsonscan to AST/decoder types.
type EventSink interface {
	OnNull() error
	OnBool(v bool) error
	OnString(v string) error
	OnNumber(rawSpan string, lexRes LexNumberResult) error
	OnObjectBegin() (EventContainerAction, error)
	OnObjectKey(key string) error
	OnObjectEnd() error
	OnArrayBegin() (EventContainerAction, error)
	OnArrayEnd() error
}

// ConfigureEventSink attaches an EventSink to a ModeEventSink Machine.
func (m *Machine) ConfigureEventSink(sink EventSink) {
	m.sink = sink
	m.sinkErr = nil
}

// SinkErr returns the user callback error that triggered StopUserAbort.
func (m *Machine) SinkErr() error {
	return m.sinkErr
}

func (m *Machine) isEventSink() bool {
	return m.Grammar.RootMode == ModeEventSink && !m.Grammar.SubModeActive && m.sink != nil
}

func (m *Machine) abortSink(src string, final bool, err error) ScanResult {
	m.sinkErr = err
	return m.finishResult(src, final, StopUserAbort, ErrNone)
}

func (m *Machine) emitLiteralToSink(litCh byte) error {
	switch litCh {
	case 't':
		return m.sink.OnBool(true)
	case 'f':
		return m.sink.OnBool(false)
	default:
		return m.sink.OnNull()
	}
}

// decodeEscapedStringCopy unquotes a single escaped string into an immutable Go
// string using m.Unesc as bounded per-token scratch (reset to [:0] immediately),
// avoiding both Unesc aliasing and document-wide scratch growth.
func (m *Machine) decodeEscapedStringCopy(
	src string,
	rawStart, firstEsc, rawContentEnd int,
	rejectLoneSurrogate bool,
	astCompatLex bool,
) (out string, errRelOff int, errCode ScanError) {
	prefixLen := firstEsc - rawStart
	maxOut := rawContentEnd - rawStart
	if cap(m.Unesc) < maxOut {
		newCap := cap(m.Unesc) * 2
		if newCap < 256 {
			newCap = 256
		}
		if newCap < maxOut {
			newCap = maxOut
		}
		m.Unesc = make([]byte, maxOut, newCap)
	} else {
		m.Unesc = m.Unesc[:maxOut]
	}
	copy(m.Unesc[:prefixLen], src[rawStart:firstEsc])
	escTail := src[firstEsc:rawContentEnd]
	w, rawErrPos, perr := kernUnquoteRaw(escTail, m.Unesc[prefixLen:maxOut], !rejectLoneSurrogate)
	if perr != 0 {
		m.Unesc = m.Unesc[:0]
		off, sc := NormalizeUnquoteError(escTail, rawErrPos, perr, rejectLoneSurrogate)
		if perr == types.ERR_EOF && astCompatLex {
			sc = ErrEOF
		}
		return "", off, sc
	}
	out = string(m.Unesc[:prefixLen+w])
	m.Unesc = m.Unesc[:0]
	return out, 0, ErrNone
}
