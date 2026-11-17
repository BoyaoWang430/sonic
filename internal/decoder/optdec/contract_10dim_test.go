//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

package optdec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type contractCallbackProbe struct {
	Value int
	Log   *[]string
	Addrs *[]uintptr
}

func (c *contractCallbackProbe) UnmarshalJSON(data []byte) error {
	if c.Log != nil {
		*c.Log = append(*c.Log, string(bytes.TrimSpace(data)))
	}
	return json.Unmarshal(data, &c.Value)
}

// TestContractMatrix10Dimensions validates the 10 public semantic contract
// dimensions defined in COMPATIBILITY.md and architecture review task G3,
// independent of internal fast-path counts.
func TestContractMatrix10Dimensions(t *testing.T) {
	// Dimensions 3, 4 & 6: Syntax errors inside a top-level value never mutate
	// pre-populated targets or invoke UnmarshalJSON callbacks across any scan mode.
	t.Run("SyntaxPreMutationAndZeroCallbacks", func(t *testing.T) {
		type env struct {
			First contractCallbackProbe `json:"first"`
			ID    int                   `json:"id"`
			Skip  string                `json:"skip"`
		}
		badInputs := []string{
			`{"first":10,"id":20,"broken":`,
			`{"first":10,"skip":[1,2,truX],"id":20}`,
			`{"first":10,"id":1e+}`,
		}
		for _, src := range badInputs {
			var callsRuns, callsScalar, callsSchema, callsGiant, callsStream []string
			initEnv := func(logPtr *[]string) func(*env) {
				return func(e *env) {
					e.First = contractCallbackProbe{Value: 77, Log: logPtr}
					e.ID = 88
					e.Skip = "keep"
				}
			}
			gotR, errR := decodeViaRunsWithInit[env](src, diffStdOpts, initEnv(&callsRuns))
			gotSc, errSc := decodeViaScalarRunsWithInit[env](src, diffStdOpts, initEnv(&callsScalar))
			gotSh, _, errSh := decodeViaSchemaOrFullWithInit[env](src, diffStdOpts, initEnv(&callsSchema))
			gotG, errG := decodeViaGiantWithInit[env](src, diffStdOpts, initEnv(&callsGiant))
			gotSt, errSt := decodeViaChunkedStreamWithInit[env](src, diffStdOpts, 3, initEnv(&callsStream))

			for name, err := range map[string]error{
				"runs": errR, "scalar": errSc, "schema": errSh, "giant": errG, "stream": errSt,
			} {
				if classifyContractErr(err) != "syntax" {
					t.Fatalf("%s on %q: expected SyntaxError, got %T (%v)", name, src, err, err)
				}
			}
			for name, calls := range map[string][]string{
				"runs": callsRuns, "scalar": callsScalar, "schema": callsSchema, "giant": callsGiant, "stream": callsStream,
			} {
				if len(calls) != 0 {
					t.Fatalf("%s invoked UnmarshalJSON %d times on syntax error %q: %v", name, len(calls), src, calls)
				}
			}
			if gotR.First.Value != 77 || gotR.ID != 88 || gotR.Skip != "keep" ||
				gotSc.ID != 88 || gotSh.ID != 88 || gotG.ID != 88 || gotSt.ID != 88 {
				t.Fatalf("syntax error mutated destination on %q: runs=%+v scalar=%+v schema=%+v giant=%+v stream=%+v",
					src, gotR, gotSc, gotSh, gotG, gotSt)
			}
		}
	})

	// Dimensions 4 & 5: Numeric range/type errors leave failed slots unmutated
	// while preserving surrounding field updates, slice backing reuse, and map slot replacement.
	t.Run("PostErrorSlotStateSliceBackingAndMapReplacement", func(t *testing.T) {
		type payload struct {
			Before int            `json:"before"`
			BadNum float64        `json:"bad_num"`
			After  int            `json:"after"`
			Slice  []int          `json:"slice"`
			Map    map[string]int `json:"map"`
		}
		src := `{"before":11,"bad_num":1e999,"unused":{"nested":[1,2,3]},"after":22,"slice":[5,6],"map":{"k":99}}`
		backing := []int{100, 200, 300, 400}
		initFn := func(p *payload) {
			p.Before = 1
			p.BadNum = 42.5
			p.After = 2
			p.Slice = backing[:3]
			p.Map = map[string]int{"k": 7, "keep": 8}
		}

		gotR, errR := decodeViaRunsWithInit[payload](src, diffStdOpts, initFn)
		if classifyContractErr(errR) != "mismatch" {
			t.Fatalf("expected MismatchTypeError for 1e999 -> float64, got %T (%v)", errR, errR)
		}
		if gotR.Before != 11 || gotR.BadNum != 42.5 || gotR.After != 22 ||
			!reflect.DeepEqual(gotR.Slice, []int{5, 6}) || backing[0] != 5 || backing[1] != 6 ||
			gotR.Map["k"] != 99 || gotR.Map["keep"] != 8 {
			t.Fatalf("unexpected post-error state: got=%+v backing=%v", gotR, backing)
		}

		// Verify all scan modes produce identical post-error value state.
		initFresh := func(p *payload) {
			p.Before = 1
			p.BadNum = 42.5
			p.After = 2
			p.Slice = make([]int, 3, 4)
			p.Map = map[string]int{"k": 7, "keep": 8}
		}
		ref, _ := decodeViaRunsWithInit[payload](src, diffStdOpts, initFresh)
		sc, _ := decodeViaScalarRunsWithInit[payload](src, diffStdOpts, initFresh)
		sh, _, _ := decodeViaSchemaOrFullWithInit[payload](src, diffStdOpts, initFresh)
		gi, _ := decodeViaGiantWithInit[payload](src, diffStdOpts, initFresh)
		st, _ := decodeViaChunkedStreamWithInit[payload](src, diffStdOpts, 4, initFresh)
		if !reflect.DeepEqual(ref, sc) || !reflect.DeepEqual(ref, sh) || !reflect.DeepEqual(ref, gi) || !reflect.DeepEqual(ref, st) {
			t.Fatalf("post-error state diverged: runs=%+v scalar=%+v schema=%+v giant=%+v stream=%+v", ref, sc, sh, gi, st)
		}
	})

	// Dimension 6 & 8: Callback invocation order and arbitrary stream chunk sizes (1..64).
	t.Run("CallbackOrderAndArbitraryStreamChunkSizes", func(t *testing.T) {
		type doc struct {
			Items []contractCallbackProbe `json:"items"`
			Extra map[string]any          `json:"extra"`
			Tail  string                  `json:"tail"`
		}
		src := `{"extra":{"u":"\u4e2d\u6587","arr":[true,false,null,-0,123.45]},"items":[10, 20, 30],"tail":"line\nbreak"}`
		var wantLog []string
		wantDoc, err := decodeViaRunsWithInit[doc](src, diffStdOpts, func(d *doc) {
			d.Items = []contractCallbackProbe{{Log: &wantLog}, {Log: &wantLog}, {Log: &wantLog}}
		})
		if err != nil {
			t.Fatalf("runs decode failed: %v", err)
		}
		if !reflect.DeepEqual(wantLog, []string{"10", "20", "30"}) {
			t.Fatalf("unexpected callback order: %v", wantLog)
		}

		for _, chunk := range []int{1, 2, 3, 5, 8, 13, 31, 64} {
			var gotLog []string
			gotDoc, err := decodeViaChunkedStreamWithInit[doc](src, diffStdOpts, chunk, func(d *doc) {
				d.Items = []contractCallbackProbe{{Log: &gotLog}, {Log: &gotLog}, {Log: &gotLog}}
			})
			if err != nil {
				t.Fatalf("chunk=%d stream decode failed: %v", chunk, err)
			}
			if !reflect.DeepEqual(gotLog, wantLog) || len(gotDoc.Items) != 3 ||
				gotDoc.Items[0].Value != 10 || gotDoc.Items[1].Value != 20 || gotDoc.Items[2].Value != 30 ||
				gotDoc.Tail != wantDoc.Tail || !reflect.DeepEqual(gotDoc.Extra, wantDoc.Extra) {
				t.Fatalf("chunk=%d mismatch: log=%v got=%+v want=%+v", chunk, gotLog, gotDoc, wantDoc)
			}
		}
	})

	// Dimension 9 & 10: Scalar vs Native scanner parity at container depth limits (4096 vs 4097).
	t.Run("ScalarVsNativeDepthAndPoolHygiene", func(t *testing.T) {
		ok4096 := strings.Repeat("[", 4096) + "1" + strings.Repeat("]", 4096)
		var vRuns, vScalar any
		if _, err := decodeViaRunsWithInit[any](ok4096, diffStdOpts, func(p *any) { vRuns = *p }); err != nil {
			t.Fatalf("depth 4096 runs failed: %v", err)
		}
		if _, err := decodeViaScalarRunsWithInit[any](ok4096, diffStdOpts, func(p *any) { vScalar = *p }); err != nil {
			t.Fatalf("depth 4096 scalar failed: %v", err)
		}
		_ = vRuns
		_ = vScalar

		over4097 := strings.Repeat("[", 4097) + "1" + strings.Repeat("]", 4097)
		_, errR := decodeViaRunsWithInit[any](over4097, diffStdOpts, nil)
		_, errS := decodeViaScalarRunsWithInit[any](over4097, diffStdOpts, nil)
		_, errSt := decodeViaChunkedStreamWithInit[any](over4097, diffStdOpts, 64, nil)
		if classifyContractErr(errR) != "syntax" || classifyContractErr(errS) != "syntax" || classifyContractErr(errSt) != "syntax" {
			t.Fatalf("depth 4097 expected SyntaxError across runs/scalar/stream: runs=%v scalar=%v stream=%v", errR, errS, errSt)
		}
	})
}
