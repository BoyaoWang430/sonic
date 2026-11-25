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
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/cpu"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/stretchr/testify/require"
)

func TestG4EagerConsumersSharedJSONScanContracts(t *testing.T) {
	origScalar := cpu.UseScalar
	defer func() { cpu.UseScalar = origScalar }()

	for _, forceScalar := range []bool{false, true} {
		cpu.UseScalar = forceScalar

		// 1. Single-value prefix boundary (FullDocument=false).
		consumed, val, err := Loads("  {\"k\":[1,true,null,\"v\\n\"]}  trailing_ignored")
		require.NoError(t, err)
		require.Equal(t, 27, consumed)
		require.Equal(t, map[string]interface{}{
			"k": []interface{}{float64(1), true, nil, "v\n"},
		}, val)

		// 2. Numbers: -0, 64-bit unsigned (>MaxInt64), >20-digit, and float64 overflow.
		_, negZero, err := Loads("-0")
		require.NoError(t, err)
		fNegZero, ok := negZero.(float64)
		require.True(t, ok)
		require.True(t, math.Signbit(fNegZero) && fNegZero == 0)

		_, u64MaxUseNum, err := LoadsUseNumber("18446744073709551615")
		require.NoError(t, err)
		require.Equal(t, json.Number("18446744073709551615"), u64MaxUseNum)

		// Float64 overflow returns *strconv.NumError in !useNumber, succeeds in useNumber,
		// and does not corrupt raw Node to V_ERROR.
		_, _, ovErr := Loads("1e999")
		var numErr *strconv.NumError
		require.ErrorAs(t, ovErr, &numErr)

		rawOverflow := NewRaw("[1e999]")
		_, arrErr := rawOverflow.Array()
		require.ErrorAs(t, arrErr, &numErr)
		require.NotEqual(t, V_ERROR, rawOverflow.Type())
		arrUseNum, arrNumErr := rawOverflow.ArrayUseNumber()
		require.NoError(t, arrNumErr)
		require.Equal(t, []interface{}{json.Number("1e999")}, arrUseNum)

		// 3. Relaxed control chars (<0x20) + lone surrogate replacement + >64KiB escaped string.
		bigEsc := `"` + strings.Repeat("a", 70000) + `\ud800` + "\x01" + `\n"`
		cBig, sBig, err := Loads(bigEsc)
		require.NoError(t, err)
		require.Equal(t, len(bigEsc), cBig)
		strBig := sBig.(string)
		require.Equal(t, 70000+len("\ufffd")+2, len(strBig))
		require.True(t, strings.HasSuffix(strBig, "\ufffd\x01\n"))

		// 4. Container nesting > 32 (overflowing inline 32-frame stack) succeeds;
		// nesting > 4096 fails cleanly with ERR_RECURSE_EXCEED_MAX without goroutine stack overflow.
		const okDepth = 48
		deepOK := strings.Repeat("[", okDepth) + "42" + strings.Repeat("]", okDepth)
		_, deepVal, err := Loads(deepOK)
		require.NoError(t, err)
		require.NotNil(t, deepVal)

		tooDeep := strings.Repeat("[", 4097) + "0" + strings.Repeat("]", 4097)
		_, _, errDeep := Loads(tooDeep)
		require.Error(t, errDeep)
		require.Contains(t, errDeep.Error(), types.ERR_RECURSE_EXCEED_MAX.Message())

		// 5. Syntax error mutates raw Node to V_ERROR on rawMap/rawArray/rawInterface.
		badObj := NewRaw(`{"a": ]}`)
		_, errBadObj := badObj.Map()
		require.Error(t, errBadObj)
		require.Equal(t, V_ERROR, badObj.Type())
	}
}

type reentrantTestVisitor struct {
	t         *testing.T
	events    []string
	customErr error
	reentered bool
}

func (v *reentrantTestVisitor) OnNull() error {
	v.events = append(v.events, "null")
	return nil
}

func (v *reentrantTestVisitor) OnBool(b bool) error {
	if b {
		v.events = append(v.events, "true")
	} else {
		v.events = append(v.events, "false")
	}
	return nil
}

func (v *reentrantTestVisitor) OnString(s string) error {
	v.events = append(v.events, "str:"+s)
	if !v.reentered {
		v.reentered = true
		// Re-entrant Preorder + Loads inside an active callback must operate on independent state (G4 §8.5).
		var nested reentrantTestVisitor
		nested.t = v.t
		nested.reentered = true
		require.NoError(v.t, Preorder(`{"inner":[10,false]}`, &nested, nil))
		require.Equal(v.t, []string{"{", "key:inner", "[", "i64:10:10", "false", "]", "}"}, nested.events)

		_, loaded, err := Loads(`{"re":true}`)
		require.NoError(v.t, err)
		require.Equal(v.t, map[string]interface{}{"re": true}, loaded)
	}
	return v.customErr
}

func (v *reentrantTestVisitor) OnInt64(i int64, n json.Number) error {
	v.events = append(v.events, "i64:"+strconv.FormatInt(i, 10)+":"+string(n))
	return nil
}

func (v *reentrantTestVisitor) OnFloat64(f float64, n json.Number) error {
	v.events = append(v.events, "f64:"+strconv.FormatFloat(f, 'g', -1, 64)+":"+string(n))
	return nil
}

func (v *reentrantTestVisitor) OnObjectBegin(_ int) error {
	v.events = append(v.events, "{")
	return nil
}

func (v *reentrantTestVisitor) OnObjectKey(k string) error {
	v.events = append(v.events, "key:"+k)
	return nil
}

func (v *reentrantTestVisitor) OnObjectEnd() error {
	v.events = append(v.events, "}")
	return nil
}

func (v *reentrantTestVisitor) OnArrayBegin(_ int) error {
	v.events = append(v.events, "[")
	return nil
}

func (v *reentrantTestVisitor) OnArrayEnd() error {
	v.events = append(v.events, "]")
	return nil
}

func TestG4PreorderReentrancyNumbersAndErrors(t *testing.T) {
	doc := `{"s":"hi\n","z":-0,"u":18446744073709551615,"f":1.5}`

	var vDefault reentrantTestVisitor
	vDefault.t = t
	require.NoError(t, Preorder(doc, &vDefault, nil))
	require.True(t, vDefault.reentered)
	require.Equal(t, []string{
		"{",
		"key:s", "str:hi\n",
		"key:z", "i64:0:-0",
		"key:u", "f64:1.8446744073709552e+19:18446744073709551615",
		"key:f", "f64:1.5:1.5",
		"}",
	}, vDefault.events)

	var vOnlyNum reentrantTestVisitor
	vOnlyNum.t = t
	vOnlyNum.reentered = true
	require.NoError(t, Preorder(`[42, 1e999]`, &vOnlyNum, &VisitorOptions{OnlyNumber: true}))
	require.Equal(t, []string{"[", "f64:0:42", "f64:0:1e999", "]"}, vOnlyNum.events)

	// Without OnlyNumber, 1e999 returns types.ERR_FLOAT_INFINITY.
	var vOv reentrantTestVisitor
	vOv.t = t
	vOv.reentered = true
	require.Equal(t, types.ERR_FLOAT_INFINITY, Preorder(`[1e999]`, &vOv, nil))

	// User callback error is returned verbatim.
	sentinel := errors.New("stop-walk")
	var vAbort reentrantTestVisitor
	vAbort.t = t
	vAbort.reentered = true
	vAbort.customErr = sentinel
	require.ErrorIs(t, Preorder(`["boom", 1]`, &vAbort, nil), sentinel)
	require.Equal(t, []string{"[", "str:boom"}, vAbort.events)
}

func TestG4LazyNodeAddressStabilityAndLocalSetPreserved(t *testing.T) {
	src := `{"keep":{"nested":[10,20,30]},"untouched":{"huge":[1,2,3,4]}}`
	root, perr := NewParser(src).Parse()
	require.Equal(t, types.ParsingError(0), perr)

	// Capture child pointer before full materialization; LoadAll must preserve existing child slots.
	keepPtrBefore := root.Get("keep")
	require.True(t, keepPtrBefore.Exists())
	elem1Before := keepPtrBefore.Get("nested").Index(1)
	require.True(t, elem1Before.Exists())

	require.NoError(t, root.LoadAll())
	keepPtrAfter := root.Get("keep")
	elem1After := keepPtrAfter.Get("nested").Index(1)
	require.Same(t, keepPtrBefore, keepPtrAfter)
	require.Same(t, elem1Before, elem1After)

	v, err := elem1After.Int64()
	require.NoError(t, err)
	require.Equal(t, int64(20), v)

	// Local raw-object Set keeps unrelated sibling as _V_RAW without full-document tape decode.
	rawRoot := NewRaw(src)
	existed, err := rawRoot.Set("added", NewNumber("99"))
	require.NoError(t, err)
	require.False(t, existed)
	untouched := rawRoot.Get("untouched")
	require.True(t, untouched.Exists())
	require.NotZero(t, untouched.loadt()&_V_RAW)
}
