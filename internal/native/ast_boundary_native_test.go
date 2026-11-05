//go:build arm64 && go1.20 && !go1.28

package native

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
)

// This file can be copied unchanged into a checkout using the generated NEON
// backend. It deliberately calls only the native API, never the Go scanners or
// their frozen references. Compare C, unchanged Go, and candidate Go separately:
// existing C/Go differences must not silently become optimization regressions.
//
// Set SONIC_AST_ORACLE_OUT to write deterministic JSONL. Source is []byte so JSON
// preserves invalid UTF-8 as base64. Float results use their exact bit patterns.
// There is no cursor clamping, error rewriting, or state-stack normalization.
// Frozen from the unchanged shared Go backend before the ARM64 scan-boundary
// changes. The C records are kept separately: legacy EOF cursors can exceed
// the logical input length, whereas this backend's accesses/cursors are bounded.
const astBoundaryGolden = "d0d9890199e986afe12deb0cf9168806da295fb43b808794cbb1587a2d3e46b3"

type astBoundaryInput struct {
	name   string
	source string
	start  int
}

type astBoundaryValue struct {
	Type       int64  `json:"type"`
	Int        int64  `json:"int"`
	FloatBits  uint64 `json:"float_bits"`
	Escape     int    `json:"escape"`
	DigitCap   int    `json:"digit_cap"`
	SameBuffer bool   `json:"same_buffer"`
}

type astBoundaryRecord struct {
	ID     int               `json:"id"`
	Case   string            `json:"case"`
	Source []byte            `json:"source"`
	Start  int               `json:"start"`
	Op     string            `json:"op"`
	Flags  uint64            `json:"flags"`
	Path   string            `json:"path,omitempty"`
	Ret    int               `json:"ret"`
	Cursor int               `json:"cursor"`
	Sp     int               `json:"sp"`
	Stack  []int             `json:"stack,omitempty"`
	Value  *astBoundaryValue `json:"value,omitempty"`
}

func astBoundaryInputs() []astBoundaryInput {
	var inputs []astBoundaryInput
	add := func(name, source string, start int) {
		inputs = append(inputs, astBoundaryInput{name, source, start})
	}
	for i, source := range []string{
		"", " ", "\t\r\n", "\x00", "\v", "null", "true", "false", "nul", "tru", "fals",
		"nulX", "trux", "falsX", "true tail", "0", "-0", "-0.0", "01", "+1", "-", "1.",
		"1e", "1e+", "1e-", "1+2", "1.2.3", "1e2e3", "1e999", "-1e-999",
		"9223372036854775807", "9223372036854775808", "18446744073709551615",
		"18446744073709551616", "-9223372036854775808", "-9223372036854775809",
		"0." + strings.Repeat("1234567890", 90),
		`""`, `"plain"`, `"a\nb\\c\"d"`, `"\q"`, `"\u12"`, `"\u12x4"`,
		`"\ud800"`, `"\ud800\udc00"`, `"\ud800\ud800"`, `"\udc00"`,
		`"unterminated`, `"tail\`, "\"raw\ncontrol\"", "\"nul\x00byte\"", "\"\xff\xc0\xaf\"",
		"[]", "{}", "[", "{", "[1,]", `{"a":}`, `{"a":1,}`, `{"a" 1}`, "[1 2]",
		"[}", "{]}", "[}]", "[invalid,]", ":", ",", "]", "}",
		`{"target":1,"target":2}`, `{"":0,"target":[1,true,null,{"x":"v"}]}`,
		`{"noise":[invalid],"target":7}`, `{"noise":nulX,"target":7}`,
		`{"x\q":0,"target":7}`, `{"\u0078\q":0,"target":7}`,
		`{"targ\u0065t":7}`, `{"targ\u0065t\q":0,"target":7}`,
		`{"\ud83d\ude0a":4}`, `[0,{"target":7},"last"]`,
	} {
		add(fmt.Sprintf("fixed/%03d", i), source, 0)
	}

	seeds := []string{
		`{"target":[0,true,false,null,"a\nb\\c\"d",{"x":-12.5e+2}],"tail":0}`,
		`["plain",{"targ\u0065t":"\ud83d\ude0a"},1e+3]`,
		" \t{\"noise\":[1,2],\"target\":\"x\"} trailing",
	}
	for seed, source := range seeds {
		for cut := 0; cut <= len(source); cut++ {
			add(fmt.Sprintf("truncate/%d/%d", seed, cut), source[:cut], 0)
		}
		for pos := 0; pos < len(source); pos++ {
			for _, value := range []byte{0, '\v', '?', '"', '\\', ':', ',', ']', '}', 0xff} {
				mutated := []byte(source)
				mutated[pos] = value
				add(fmt.Sprintf("mutate/%d/%d/%02x", seed, pos, value), string(mutated), 0)
			}
		}
	}

	// Shift events across both NEON blocks and 64-byte mask boundaries. Prefix
	// bytes are outside the initial cursor, but remain part of absolute offsets.
	for _, width := range []int{0, 1, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129} {
		plain := strings.Repeat("x", width)
		white := strings.Repeat(" \t\r\n", (width+3)/4)[:width]
		for _, offset := range []int{0, 1, 7, 15} {
			prefix := strings.Repeat("p", offset)
			for variant, body := range []string{
				`{"noise":"` + plain + `\"tail","target":7}`,
				`{"` + plain + `":0,"target":"` + plain + `\n"}`,
				`{"target"` + white + ":" + white + `[true,{},[],"x"]}`,
				`["` + plain + `\u0041",{"target":7}]`,
				`{"noise":"` + plain + "\x00" + `","target":7}`,
				white,
			} {
				add(fmt.Sprintf("boundary/%d/%d/%d", width, offset, variant), prefix+body, offset)
			}
		}
	}
	for _, width := range []int{128, 4096} {
		key := strings.Repeat("k", width)
		add(fmt.Sprintf("long-key/%d", width), `{"`+key+`":0,"target":7}`, 0)
	}
	for _, depth := range []int{1, 31, 32, 63, 64, 127, 128, types.MAX_RECURSE - 1, types.MAX_RECURSE, types.MAX_RECURSE + 1} {
		prefix, suffix := strings.Repeat("[", depth), strings.Repeat("]", depth)
		add(fmt.Sprintf("depth/array/%d", depth), prefix+"0"+suffix, 0)
		add(fmt.Sprintf("depth/second/%d", depth), prefix+"0,0"+suffix, 0)
		add(fmt.Sprintf("depth/truncated/%d", depth), prefix+"0", 0)
	}
	for _, depth := range []int{31, 127, types.MAX_RECURSE - 1, types.MAX_RECURSE} {
		add(fmt.Sprintf("depth/object/%d", depth), strings.Repeat(`{"x":`, depth)+"0"+strings.Repeat("}", depth), 0)
	}
	return inputs
}

func TestASTNativeBoundaryOracle(t *testing.T) {
	digest := sha256.New()
	var writer io.Writer = digest
	if path := os.Getenv("SONIC_AST_ORACLE_OUT"); path != "" {
		file, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		buffer := bufio.NewWriter(file)
		t.Cleanup(func() {
			if err := buffer.Flush(); err != nil {
				t.Error(err)
			}
			if err := file.Close(); err != nil {
				t.Error(err)
			}
		})
		writer = io.MultiWriter(digest, buffer)
	}
	encoder := json.NewEncoder(writer)
	count := 0
	emit := func(record astBoundaryRecord) {
		t.Helper()
		record.ID = count
		count++
		if count > 100000 {
			t.Fatal("oracle corpus exceeded 100000 records")
		}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	type namedString string
	type namedInt int
	paths := []struct {
		name string
		path []interface{}
	}{
		{"empty", nil},
		{"target", []interface{}{"target"}},
		{"missing", []interface{}{"missing"}},
		{"index0", []interface{}{0}},
		{"index1.target", []interface{}{1, "target"}},
		{"target.index0", []interface{}{"target", 0}},
		{"empty-key", []interface{}{""}},
		{"emoji-key", []interface{}{"😊"}},
		{"named-target", []interface{}{namedString("target")}},
		{"named-index0", []interface{}{namedInt(0)}},
		{"negative-index", []interface{}{-1}},
		{"int64-index", []interface{}{int64(0)}},
		{"nil-component", []interface{}{nil}},
	}
	const relaxed = uint64(1 << types.B_NO_VALIDATE_JSON)
	var machine types.StateMachine
	var digits [types.MaxDigitNums]byte
	for inputIndex, input := range astBoundaryInputs() {
		// Keep the logical length unchanged while providing the legacy C
		// scanner's readable tail. The Go backend still sees bounded input.
		padded := make([]byte, len(input.source)+types.BufPaddingSize)
		copy(padded, input.source)
		source := rt.String(&padded[0], len(input.source))
		base := astBoundaryRecord{Case: input.name, Source: []byte(input.source), Start: input.start, Sp: -1}
		run := func(op string, flags uint64, pathName string, path []interface{}, strict bool) {
			record := base
			record.Op, record.Flags, record.Path = op, flags, pathName
			pos := input.start
			// A stale active slot checks API initialization. Only the active
			// output stack is part of the record; unused capacity is ignored.
			machine.Sp, machine.Vt[0] = 1, -777
			state := &machine
			switch op {
			case "skip":
				record.Ret = SkipOne(&source, &pos, state, flags)
			case "fast":
				state = nil
				record.Ret = SkipOneFast(&source, &pos)
			case "validate":
				record.Ret = ValidateOne(&source, &pos, state, flags)
			case "path":
				if !strict {
					state = nil
				}
				record.Ret = GetByPath(&source, &pos, &path, state)
			case "value":
				state = nil
				value := types.JsonState{Dbuf: &digits[0], Dcap: len(digits)}
				pos = Value(unsafe.Pointer(&padded[0]), len(source), pos, &value, flags)
				record.Ret = pos
				record.Value = &astBoundaryValue{value.Vt, value.Iv, math.Float64bits(value.Dv), value.Ep, value.Dcap, value.Dbuf == &digits[0]}
			}
			record.Cursor = pos
			if state != nil {
				record.Sp = state.Sp
				if state.Sp < 0 || state.Sp > len(state.Vt) {
					t.Fatalf("invalid output stack: case=%s op=%s sp=%d", input.name, op, state.Sp)
				}
				record.Stack = state.Vt[:state.Sp]
			}
			emit(record)
		}
		for _, flags := range []uint64{0, types.F_VALIDATE_STRING, relaxed} {
			run("skip", flags, "", nil, false)
		}
		run("fast", 0, "", nil, false)
		for _, flags := range []uint64{0, types.F_VALIDATE_STRING, relaxed, relaxed | types.F_VALIDATE_STRING} {
			run("validate", flags, "", nil, false)
		}
		for _, flags := range []uint64{0, types.F_USE_NUMBER, types.F_VALIDATE_STRING, types.F_ALLOW_CONTROL} {
			run("value", flags, "", nil, false)
		}
		// Exercise every path on fixed cases; sample three complementary paths
		// per generated case to keep the mutation corpus below 100k records.
		selected := paths
		if !strings.HasPrefix(input.name, "fixed/") {
			selected = []struct {
				name string
				path []interface{}
			}{paths[1], paths[3], paths[inputIndex%len(paths)]}
		}
		for _, path := range selected {
			for _, strict := range []bool{false, true} {
				name := path.name + "/fast"
				if strict {
					name = path.name + "/strict"
				}
				run("path", 0, name, path.path, strict)
			}
		}
		runtime.KeepAlive(padded)
	}
	got := fmt.Sprintf("%x", digest.Sum(nil))
	t.Logf("AST native oracle: records=%d sha256=%s", count, got)
	if astBoundaryGolden != "" && got != astBoundaryGolden {
		t.Fatalf("AST native oracle digest=%s, want %s", got, astBoundaryGolden)
	}
}
