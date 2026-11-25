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

package optdec

import (
	"unsafe"

	"github.com/bytedance/sonic/internal/jsonscan"
)

// Compile-time layout & constant assertions guaranteeing zero-copy []node <-> []jsonscan.Node views.
var (
	_ [unsafe.Sizeof(node{}) - unsafe.Sizeof(jsonscan.Node{})]byte
	_ [unsafe.Sizeof(jsonscan.Node{}) - unsafe.Sizeof(node{})]byte
	_ [unsafe.Offsetof(node{}.typ) - unsafe.Offsetof(jsonscan.Node{}.Typ)]byte
	_ [unsafe.Offsetof(node{}.val) - unsafe.Offsetof(jsonscan.Node{}.Val)]byte

	_ [KNull - int(jsonscan.KNull)]byte
	_ [int(jsonscan.KNull) - KNull]byte
	_ [KFalse - int(jsonscan.KFalse)]byte
	_ [int(jsonscan.KFalse) - KFalse]byte
	_ [KTrue - int(jsonscan.KTrue)]byte
	_ [int(jsonscan.KTrue) - KTrue]byte
	_ [KUint - int(jsonscan.KUint)]byte
	_ [int(jsonscan.KUint) - KUint]byte
	_ [KSint - int(jsonscan.KSint)]byte
	_ [int(jsonscan.KSint) - KSint]byte
	_ [KReal - int(jsonscan.KReal)]byte
	_ [int(jsonscan.KReal) - KReal]byte
	_ [KRawNumber - int(jsonscan.KRawNumber)]byte
	_ [int(jsonscan.KRawNumber) - KRawNumber]byte
	_ [KStringCommon - int(jsonscan.KStringCommon)]byte
	_ [int(jsonscan.KStringCommon) - KStringCommon]byte
	_ [KStringEscaped - int(jsonscan.KStringEscaped)]byte
	_ [int(jsonscan.KStringEscaped) - KStringEscaped]byte
	_ [KRaw - int(jsonscan.KRaw)]byte
	_ [int(jsonscan.KRaw) - KRaw]byte
	_ [KObject - int(jsonscan.KObject)]byte
	_ [int(jsonscan.KObject) - KObject]byte
	_ [KArray - int(jsonscan.KArray)]byte
	_ [int(jsonscan.KArray) - KArray]byte
)

// nodesAsJSONScanWritable exposes nodes[:writableNodesCap(nodes)] to
// jsonscan.Machine so at least nodeGuardSlots trailing guard node slot always
// remains in-bounds inside physicalNodesCap(nodes) after Produced (preventing
// PtrOffset(lastNode, 1) from pointing to Go GC span.limit).
func nodesAsJSONScanWritable(nodes []node) []jsonscan.Node {
	w := writableNodesCap(nodes)
	if w <= 0 {
		return nil
	}
	usable := nodes[:w]
	return unsafe.Slice((*jsonscan.Node)(unsafe.Pointer(unsafe.SliceData(usable))), len(usable))
}

func statFromJSONScan(st jsonscan.Stat) jsonStat {
	return jsonStat{
		object:      st.Object,
		array:       st.Array,
		str:         st.Str,
		number:      st.Number,
		array_elems: st.ArrayElems,
		object_keys: st.ObjectKeys,
		max_depth:   st.MaxDepth,
	}
}

func strictProfileFromOpts(opts uint64, fullDoc bool) jsonscan.Profile {
	useNumber := (opts & (1 << _F_use_number)) != 0
	validateStr := (opts & (1 << _F_validate_string)) != 0
	strictURC := (opts & (1 << _F_disable_urc)) != 0
	return jsonscan.StrictProfile(validateStr, validateStr, strictURC, useNumber, !useNumber, fullDoc)
}

func mapJSONScanError(code jsonscan.ScanError) ErrorCode {
	switch code {
	case jsonscan.ErrNone:
		return SONIC_OK
	case jsonscan.ErrControlChar:
		return SONIC_CONTROL_CHAR
	case jsonscan.ErrInvalidEscape:
		return SONIC_INVALID_ESCAPED
	case jsonscan.ErrInvalidNumber:
		return SONIC_INVALID_NUM
	case jsonscan.ErrFloatInf:
		return SONIC_FLOAT_INF
	case jsonscan.ErrEOF:
		return SONIC_EOF
	case jsonscan.ErrInvalidChar:
		return SONIC_INVALID_CHAR
	case jsonscan.ErrExpectKey:
		return SONIC_EXPECT_KEY
	case jsonscan.ErrExpectColon:
		return SONIC_EXPECT_COLON
	case jsonscan.ErrExpectObjCommaOrEnd:
		return SONIC_EXPECT_OBJ_COMMA_OR_END
	case jsonscan.ErrExpectArrCommaOrEnd:
		return SONIC_EXPECT_ARR_COMMA_OR_END
	case jsonscan.ErrInvalidEscapeUTF:
		return SONIC_INVALID_ESCAPED_UTF
	case jsonscan.ErrInvalidLiteral:
		return SONIC_INVALID_LITERAL
	case jsonscan.ErrDepthOverflow, jsonscan.ErrSpanOverflow:
		return SONIC_STACK_OVERFLOW
	default:
		return SONIC_INVALID_CHAR
	}
}
