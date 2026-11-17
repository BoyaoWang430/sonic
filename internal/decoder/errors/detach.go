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

package errors

import (
	"encoding/json"
	"strconv"
	"unsafe"
)

func cloneIfAliasesBytes(s string, b0, bEnd uintptr, borrowed []byte) (string, bool) {
	if len(s) == 0 {
		return s, false
	}
	sp := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	if sp < b0 || sp >= bEnd {
		return s, false
	}
	if sp == b0 && len(s) == len(borrowed) {
		return string(borrowed), true
	}
	return string([]byte(s)), true
}

// DetachSource ensures that any string fields held by err do not alias borrowed
// (such as a caller-owned []byte passed to sonic.Unmarshal or a sliding stream
// buffer in StreamDecoder). Errors constructed by user callbacks whose string
// fields do not alias borrowed are returned unchanged.
func DetachSource(err error, borrowed []byte) error {
	if err == nil || len(borrowed) == 0 {
		return err
	}
	b0 := uintptr(unsafe.Pointer(unsafe.SliceData(borrowed)))
	bEnd := b0 + uintptr(len(borrowed))

	switch e := err.(type) {
	case SyntaxError:
		if cloned, ok := cloneIfAliasesBytes(e.Src, b0, bEnd, borrowed); ok {
			e.Src = cloned
			return e
		}
	case *SyntaxError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBytes(e.Src, b0, bEnd, borrowed); ok {
				cp := *e
				cp.Src = cloned
				return &cp
			}
		}
	case MismatchTypeError:
		if cloned, ok := cloneIfAliasesBytes(e.Src, b0, bEnd, borrowed); ok {
			e.Src = cloned
			return e
		}
	case *MismatchTypeError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBytes(e.Src, b0, bEnd, borrowed); ok {
				cp := *e
				cp.Src = cloned
				return &cp
			}
		}
	case *json.UnmarshalTypeError:
		if e != nil {
			valCloned, okV := cloneIfAliasesBytes(e.Value, b0, bEnd, borrowed)
			fldCloned, okF := cloneIfAliasesBytes(e.Field, b0, bEnd, borrowed)
			if okV || okF {
				cp := *e
				if okV {
					cp.Value = valCloned
				}
				if okF {
					cp.Field = fldCloned
				}
				return &cp
			}
		}
	case *strconv.NumError:
		if e != nil {
			if cloned, ok := cloneIfAliasesBytes(e.Num, b0, bEnd, borrowed); ok {
				cp := *e
				cp.Num = cloned
				return &cp
			}
		}
	}
	return err
}
