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
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unsafe"
)

func stdDecodeUseNumberForTest(src string, dst interface{}) error {
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	return dec.Decode(dst)
}

func TestDecodeEfaceUseNumberNoCopyParity(t *testing.T) {
	cases := []string{
		`0`,
		`-0`,
		`1`,
		`9`,
		`10`,
		`99`,
		`100`,
		`999`,
		`1000`,
		`1023`,
		`1024`,
		`-1`,
		`-9`,
		`-10`,
		`-64`,
		`-65`,
		`-100`,
		`0.0`,
		`-0.0`,
		`1.0`,
		`1.5`,
		`-1.5`,
		`1e0`,
		`1e5`,
		`1e400`,
		`-1e400`,
		`9007199254740993`,
		`18446744073709551615`,
		`999999999999999999999999999999`,
		`""`,
		`"a"`,
		`"0"`,
		`"é"`,
		`"hello\n\"world\"\\u4e2d"`,
		`true`,
		`false`,
		`null`,
		`[]`,
		`{}`,
		`{"zero":0,"neg_zero":-0,"pos":[0,1,9,10,99,100,999,1000,1023,1024],"neg":[-1,-9,-10,-64,-65,-100],"dec":[0.0,-0.0,1.0,1.5,-1.5,1e5,1e400],"big":9007199254740993,"u64":18446744073709551615,"s":["","a","x","é","active","active","esc\\n\"q\""],"b":[true,false,null,[],{}],"nested":{"k":{"v":42,"neg":-7,"arr":[[1,-1,"a"],[]]}}}`,
	}

	for _, src := range cases {
		var gotNoCopy, gotCopy, want interface{}
		if _, _, err := DecodeOne(src, 1<<_F_use_number, &gotNoCopy); err != nil {
			t.Fatalf("DecodeOne UseNumber src=%s failed: %v", src, err)
		}
		if _, _, err := DecodeOne(src, (1<<_F_use_number)|(1<<_F_copy_string), &gotCopy); err != nil {
			t.Fatalf("DecodeOne UseNumber|CopyString src=%s failed: %v", src, err)
		}
		if err := stdDecodeUseNumberForTest(src, &want); err != nil {
			t.Fatalf("std Decode UseNumber src=%s failed: %v", src, err)
		}
		if !reflect.DeepEqual(gotNoCopy, want) {
			t.Fatalf("UseNumber NoCopy mismatch src=%s:\n  got  = %#v\n  want = %#v", src, gotNoCopy, want)
		}
		if !reflect.DeepEqual(gotCopy, want) {
			t.Fatalf("UseNumber Copy mismatch src=%s:\n  got  = %#v\n  want = %#v", src, gotCopy, want)
		}
	}

	// Verify pre-boxed small integers keep "-0" distinct from "0" and are ready
	// directly from package init without warming AsEface first.
	if got := rawNumberToAny("-0", 0, 2); got != json.Number("-0") {
		t.Fatalf("rawNumberToAny(-0) = %#v, want json.Number(\"-0\")", got)
	}
	if got := rawNumberToAny("1023", 0, 4); got != json.Number("1023") {
		t.Fatalf("rawNumberToAny(1023) = %#v, want json.Number(\"1023\")", got)
	}
	if got := rawNumberToAny("1024", 0, 4); got != json.Number("1024") {
		t.Fatalf("rawNumberToAny(1024) = %#v, want json.Number(\"1024\")", got)
	}
	if got := rawNumberToAny("-64", 0, 3); got != json.Number("-64") {
		t.Fatalf("rawNumberToAny(-64) = %#v, want json.Number(\"-64\")", got)
	}
	if got := rawNumberToAny("-65", 0, 3); got != json.Number("-65") {
		t.Fatalf("rawNumberToAny(-65) = %#v, want json.Number(\"-65\")", got)
	}
}

func isWithinSourceString(s, src string) bool {
	if len(s) == 0 || len(src) == 0 {
		return false
	}
	base := uintptr(unsafe.Pointer(unsafe.StringData(src)))
	ptr := uintptr(unsafe.Pointer(unsafe.StringData(s)))
	return ptr >= base && ptr < base+uintptr(len(src))
}

// TestDecodeEfaceUseNumberStringOwnershipIsolation verifies that under
// UseNumber + CopyString=false, borrowed plain substrings in strAnyCache never
// leak into escaped string results (which must remain owned and detached from
// the input buffer), and owned escaped strings never break zero-copy aliasing
// for subsequent unescaped strings.
func TestDecodeEfaceUseNumberStringOwnershipIsolation(t *testing.T) {
	// Case 1: unescaped "ok" before escaped "o\u006b".
	buf1 := []byte(`["ok","o\u006b","ok","o\u006b"]`)
	src1 := unsafe.String(unsafe.SliceData(buf1), len(buf1))
	var got1 interface{}
	if _, _, err := DecodeOne(src1, 1<<_F_use_number, &got1); err != nil {
		t.Fatalf("DecodeOne case1 failed: %v", err)
	}
	arr1 := got1.([]interface{})
	s0, s1, s2, s3 := arr1[0].(string), arr1[1].(string), arr1[2].(string), arr1[3].(string)
	if s0 != "ok" || s1 != "ok" || s2 != "ok" || s3 != "ok" {
		t.Fatalf("case1 text mismatch: %q %q %q %q", s0, s1, s2, s3)
	}
	if !isWithinSourceString(s0, src1) || !isWithinSourceString(s2, src1) {
		t.Fatalf("case1 expected unescaped strings to alias input buffer")
	}
	if isWithinSourceString(s1, src1) || isWithinSourceString(s3, src1) {
		t.Fatalf("case1 escaped string unexpectedly aliased input buffer (ownership violation)")
	}
	// Overwrite source buffer and verify escaped results are unaffected.
	for i := range buf1 {
		buf1[i] = 'X'
	}
	if s1 != "ok" || s3 != "ok" {
		t.Fatalf("case1 escaped strings corrupted after source mutation: %q %q", s1, s3)
	}

	// Case 2: escaped "o\u006b" before unescaped "ok".
	buf2 := []byte(`["o\u006b","ok","o\u006b","ok"]`)
	src2 := unsafe.String(unsafe.SliceData(buf2), len(buf2))
	var got2 interface{}
	if _, _, err := DecodeOne(src2, 1<<_F_use_number, &got2); err != nil {
		t.Fatalf("DecodeOne case2 failed: %v", err)
	}
	arr2 := got2.([]interface{})
	e0, p1, e2, p3 := arr2[0].(string), arr2[1].(string), arr2[2].(string), arr2[3].(string)
	if e0 != "ok" || p1 != "ok" || e2 != "ok" || p3 != "ok" {
		t.Fatalf("case2 text mismatch: %q %q %q %q", e0, p1, e2, p3)
	}
	if isWithinSourceString(e0, src2) || isWithinSourceString(e2, src2) {
		t.Fatalf("case2 escaped string unexpectedly aliased input buffer")
	}
	if !isWithinSourceString(p1, src2) || !isWithinSourceString(p3, src2) {
		t.Fatalf("case2 unescaped string failed zero-copy aliasing contract")
	}

	// Case 3: object values mixing plain and escaped identical keys/values.
	buf3 := []byte(`{"a":"active","b":"act\u0069ve","c":"active","d":"act\u0069ve"}`)
	src3 := unsafe.String(unsafe.SliceData(buf3), len(buf3))
	var got3 interface{}
	if _, _, err := DecodeOne(src3, 1<<_F_use_number, &got3); err != nil {
		t.Fatalf("DecodeOne case3 failed: %v", err)
	}
	m3 := got3.(map[string]interface{})
	if !isWithinSourceString(m3["a"].(string), src3) || !isWithinSourceString(m3["c"].(string), src3) {
		t.Fatalf("case3 plain object values should alias input buffer")
	}
	if isWithinSourceString(m3["b"].(string), src3) || isWithinSourceString(m3["d"].(string), src3) {
		t.Fatalf("case3 escaped object values unexpectedly aliased input buffer")
	}
}
