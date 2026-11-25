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
	"strconv"
	"testing"

	"github.com/bytedance/sonic/internal/native/number"
)

type recordedNumberEvent struct {
	kind  string // "int64" | "float64"
	i64   int64
	fbits uint64
	raw   json.Number
}

type recordingNumberVisitor struct {
	events   []recordedNumberEvent
	abortAt  json.Number
	abortErr error
}

func (v *recordingNumberVisitor) OnNull() error            { return nil }
func (v *recordingNumberVisitor) OnBool(bool) error        { return nil }
func (v *recordingNumberVisitor) OnString(string) error    { return nil }
func (v *recordingNumberVisitor) OnObjectBegin(int) error  { return nil }
func (v *recordingNumberVisitor) OnObjectKey(string) error { return nil }
func (v *recordingNumberVisitor) OnObjectEnd() error       { return nil }
func (v *recordingNumberVisitor) OnArrayBegin(int) error   { return nil }
func (v *recordingNumberVisitor) OnArrayEnd() error        { return nil }

func (v *recordingNumberVisitor) OnInt64(x int64, n json.Number) error {
	v.events = append(v.events, recordedNumberEvent{kind: "int64", i64: x, raw: n})
	if v.abortErr != nil && n == v.abortAt {
		return v.abortErr
	}
	return nil
}

func (v *recordingNumberVisitor) OnFloat64(x float64, n json.Number) error {
	v.events = append(v.events, recordedNumberEvent{kind: "float64", fbits: math.Float64bits(x), raw: n})
	if v.abortErr != nil && n == v.abortAt {
		return v.abortErr
	}
	return nil
}

func TestASTFloatHintPreorderAndLoadsContract(t *testing.T) {
	// Mix of hinted short decimals (positive, negative, leading-zero, trailing-zero,
	// -0.0, boundary 15-digit mantissa), signed/unsigned integers (-0, 0, large uint64),
	// unhinted long decimals (>15 digits), and scientific exponents.
	nums := []string{
		"0",
		"-0",
		"42",
		"-7",
		"9223372036854775807",
		"-9223372036854775808",
		"9223372036854775808",
		"18446744073709551615",
		"0.0",
		"-0.0",
		"0.0000",
		"-0.0000",
		"1.2500",
		"-1.2500",
		"39.9042",
		"116.4074",
		"-0.125",
		"0.00000000000001",
		"-0.00000000000001",
		"123456789012.345",
		"-123456789012.345",
		"0.1234567890123456789",
		"-0.1234567890123456789",
		"1234567890123456.789",
		"1.5e-3",
		"-1.5e-3",
		"1e0",
		"-0e0",
		"1e308",
		"2.2250738585072014e-308",
	}

	src := "["
	for i, s := range nums {
		if i > 0 {
			src += ","
		}
		src += s
	}
	src += "]"

	// 1. Preorder exact event stream parity: (callback kind, exact bit pattern, verbatim json.Number)
	var vis recordingNumberVisitor
	if err := Preorder(src, &vis, nil); err != nil {
		t.Fatalf("Preorder failed: %v", err)
	}
	if len(vis.events) != len(nums) {
		t.Fatalf("Preorder event count=%d want %d", len(vis.events), len(nums))
	}
	for i, raw := range nums {
		ev := vis.events[i]
		if string(ev.raw) != raw {
			t.Fatalf("event[%d] raw=%q want %q", i, ev.raw, raw)
		}
		ref, perr := number.ParseValidatedNumber(raw)
		if perr != 0 {
			t.Fatalf("ParseValidatedNumber(%q) failed: %v", raw, perr)
		}
		switch ref.Kind {
		case number.Signed:
			if ev.kind != "int64" || ev.i64 != ref.Int64 {
				t.Fatalf("event[%d] (%q): got (%s,%d) want (int64,%d)", i, raw, ev.kind, ev.i64, ref.Int64)
			}
		case number.Unsigned:
			if ref.Uint64 <= math.MaxInt64 {
				if ev.kind != "int64" || ev.i64 != int64(ref.Uint64) {
					t.Fatalf("event[%d] (%q): got (%s,%d) want (int64,%d)", i, raw, ev.kind, ev.i64, int64(ref.Uint64))
				}
			} else {
				wantBits := math.Float64bits(float64(ref.Uint64))
				if ev.kind != "float64" || ev.fbits != wantBits {
					t.Fatalf("event[%d] (%q): got (%s,%#x) want (float64,%#x)", i, raw, ev.kind, ev.fbits, wantBits)
				}
			}
		default:
			wantBits := math.Float64bits(ref.Float64)
			if ev.kind != "float64" || ev.fbits != wantBits {
				t.Fatalf("event[%d] (%q): got (%s,%#x) want (float64,%#x)", i, raw, ev.kind, ev.fbits, wantBits)
			}
		}
	}

	// 2. Loads exact float64 bit parity (including -0, -0.0, 0.0, 1.2500, long decimals, exponents)
	consumed, val, err := Loads(src)
	if err != nil {
		t.Fatalf("Loads failed: %v", err)
	}
	if consumed != len(src) {
		t.Fatalf("Loads consumed=%d want %d", consumed, len(src))
	}
	arr, ok := val.([]interface{})
	if !ok || len(arr) != len(nums) {
		t.Fatalf("Loads result len mismatch: %T len=%d", val, len(arr))
	}
	for i, raw := range nums {
		got, ok := arr[i].(float64)
		if !ok {
			t.Fatalf("Loads[%d] (%q) type=%T want float64", i, raw, arr[i])
		}
		want, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			t.Fatalf("strconv.ParseFloat(%q): %v", raw, err)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("Loads[%d] (%q) bits=%#x (%g) want %#x (%g)",
				i, raw, math.Float64bits(got), got, math.Float64bits(want), want)
		}
	}

	// 3. LoadsUseNumber preserves verbatim json.Number tokens (including trailing zeros and overflow)
	useNumSrc := `[1.2500,-0.0,-0,39.9042,1e400]`
	_, unVal, err := LoadsUseNumber(useNumSrc)
	if err != nil {
		t.Fatalf("LoadsUseNumber failed: %v", err)
	}
	wantTokens := []json.Number{"1.2500", "-0.0", "-0", "39.9042", "1e400"}
	unArr := unVal.([]interface{})
	if len(unArr) != len(wantTokens) {
		t.Fatalf("LoadsUseNumber len=%d want %d", len(unArr), len(wantTokens))
	}
	for i, want := range wantTokens {
		got, ok := unArr[i].(json.Number)
		if !ok || got != want {
			t.Fatalf("LoadsUseNumber[%d]=%#v want %#v", i, unArr[i], want)
		}
	}

	// 4. Preorder OnlyNumber=true passes OnFloat64(0, raw) verbatim for all numbers including overflow
	var onlyVis recordingNumberVisitor
	if err := Preorder(useNumSrc, &onlyVis, &VisitorOptions{OnlyNumber: true}); err != nil {
		t.Fatalf("Preorder OnlyNumber failed: %v", err)
	}
	if len(onlyVis.events) != len(wantTokens) {
		t.Fatalf("Preorder OnlyNumber len=%d want %d", len(onlyVis.events), len(wantTokens))
	}
	for i, want := range wantTokens {
		ev := onlyVis.events[i]
		if ev.kind != "float64" || ev.fbits != 0 || ev.raw != want {
			t.Fatalf("Preorder OnlyNumber[%d]=%+v want (float64, 0, %q)", i, ev, want)
		}
	}

	// 5. Visitor early abort on a NumberHintFloat value stops traversal immediately and propagates error
	stopErr := errors.New("stop on hinted float")
	abortVis := recordingNumberVisitor{abortAt: json.Number("1.2500"), abortErr: stopErr}
	err = Preorder(`[0.5,1.2500,39.9042,99.5]`, &abortVis, nil)
	if !errors.Is(err, stopErr) {
		t.Fatalf("Preorder early abort err=%v want %v", err, stopErr)
	}
	if len(abortVis.events) != 2 || abortVis.events[0].raw != "0.5" || abortVis.events[1].raw != "1.2500" {
		t.Fatalf("Preorder early abort events=%+v want [0.5, 1.2500]", abortVis.events)
	}

	// 6. Overflow still fails consistently in Loads and Preorder (default mode)
	if _, _, err := Loads(`[1.25,1e400]`); err == nil {
		t.Fatal("expected Loads overflow error on 1e400")
	}
	var ovfVis recordingNumberVisitor
	if err := Preorder(`[1.25,1e400]`, &ovfVis, nil); err == nil {
		t.Fatal("expected Preorder overflow error on 1e400")
	}
}
