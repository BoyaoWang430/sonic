package ast

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"runtime"
	"strings"
	"testing"
)

func TestSearchManySemanticsAndRetention(t *testing.T) {
	doc := `{
		"dup": "first",
		"dup": "second",
		"nullable": null,
		"scalar": 42,
		"esc\"key": {"inner\n": "val_esc"},
		"arr": [10, {"k": "v1"}, [true, false]],
		"tail": "done"
	}`

	paths := [][]interface{}{
		{"tail"},                  // 0: out-of-order late field
		{"arr", 2, 1},             // 1: nested array bool
		{"arr", 1, "k"},           // 2: shared "arr" prefix
		{"arr", 99},               // 3: out-of-bounds index -> !Exists()
		{"missing_top"},           // 4: missing key -> !Exists()
		{"nullable", "sub"},       // 5: null intermediate -> !Exists() (no syntax error)
		{"scalar", 0},             // 6: scalar intermediate -> !Exists() (no syntax error)
		{"dup"},                   // 7: duplicate key -> first occurrence
		{"esc\"key", "inner\n"},   // 8: escaped keys
		{"arr"},                   // 9: ancestor path alongside descendant paths 1 & 2
		{"tail"},                  // 10: duplicate requested path
	}

	s := NewSearcher(doc)
	nodes, err := s.GetManyByPath(paths...)
	if err != nil {
		t.Fatalf("GetManyByPath unexpected err: %v", err)
	}
	if len(nodes) != len(paths) {
		t.Fatalf("got %d nodes, want %d", len(nodes), len(paths))
	}

	if v, err := nodes[0].String(); err != nil || v != "done" {
		t.Fatalf("nodes[0]=%q err=%v", v, err)
	}
	if v, err := nodes[1].Bool(); err != nil || v != false {
		t.Fatalf("nodes[1]=%v err=%v", v, err)
	}
	if v, err := nodes[2].String(); err != nil || v != "v1" {
		t.Fatalf("nodes[2]=%q err=%v", v, err)
	}
	for _, missIdx := range []int{3, 4, 5, 6} {
		if nodes[missIdx].Exists() || nodes[missIdx].TypeSafe() != V_NONE {
			t.Fatalf("nodes[%d] expected V_NONE missing, got type=%d", missIdx, nodes[missIdx].TypeSafe())
		}
	}
	if v, err := nodes[7].String(); err != nil || v != "first" {
		t.Fatalf("nodes[7] duplicate key got=%q err=%v, want \"first\"", v, err)
	}
	if v, err := nodes[8].String(); err != nil || v != "val_esc" {
		t.Fatalf("nodes[8] escaped key got=%q err=%v", v, err)
	}
	if rawArr, err := nodes[9].Raw(); err != nil || rawArr != `[10, {"k": "v1"}, [true, false]]` {
		t.Fatalf("nodes[9] ancestor span got=%q err=%v", rawArr, err)
	}
	if v, err := nodes[10].String(); err != nil || v != "done" {
		t.Fatalf("nodes[10] duplicate request got=%q err=%v", v, err)
	}

	// Syntax error must abort and return error.
	badDocs := []string{
		`{"a": truX, "b": 1}`,
		`{:"b"}`,
		`{"a": [1, 2}`,
		`{"a": "unclosed`,
	}
	for _, bd := range badDocs {
		if _, err := SearchMany(bd, SearchOptions{ValidateJSON: true}, []interface{}{"b"}, []interface{}{"a"}); err == nil {
			t.Fatalf("expected syntax error for %q", bd)
		}
	}

	// CopyReturn=true independent memory retention test (zero sibling pinning).
	runtime.GC()
	runtime.GC()
	var baseMem runtime.MemStats
	runtime.ReadMemStats(&baseMem)

	var kept Node
	func() {
		bigPayload := strings.Repeat("X", 2<<20) // 2 MiB
		bigJSON := `{"keep":"tiny","drop":"` + bigPayload + `"}`
		res, err := SearchMany(bigJSON, SearchOptions{ValidateJSON: true, CopyReturn: true},
			[]interface{}{"keep"},
			[]interface{}{"drop"},
		)
		if err != nil {
			t.Fatal(err)
		}
		kept = res[0]
	}()
	runtime.GC()
	runtime.GC()
	var afterMem runtime.MemStats
	runtime.ReadMemStats(&afterMem)
	if v, err := kept.String(); err != nil || v != "tiny" {
		t.Fatalf("kept node corrupted: %q %v", v, err)
	}
	if afterMem.HeapAlloc > baseMem.HeapAlloc+(512<<10) {
		t.Fatalf("CopyReturn retained excessive heap: before=%d after=%d", baseMem.HeapAlloc, afterMem.HeapAlloc)
	}
}

func TestPatchByPathSemanticsAndConflicts(t *testing.T) {
	orig := []byte("{\n  \"id\": 100,\n  \"meta\": { \"tag\": \"old\", \"keep\": [1, 2, 3] },\n  \"items\": [\"a\", \"b\", \"c\"]\n}")
	patched, err := PatchByPath(orig,
		PathPatch{Path: []interface{}{"items", 1}, Value: "B_NEW"},
		PathPatch{Path: []interface{}{"id"}, Value: 999},
		PathPatch{Path: []interface{}{"meta", "tag"}, Raw: true, RawValue: []byte(`{"nested":true}`)},
	)
	if err != nil {
		t.Fatalf("PatchByPath err: %v", err)
	}
	want := []byte("{\n  \"id\": 999,\n  \"meta\": { \"tag\": {\"nested\":true}, \"keep\": [1, 2, 3] },\n  \"items\": [\"a\", \"B_NEW\", \"c\"]\n}")
	if !bytes.Equal(patched, want) {
		t.Fatalf("PatchByPath exact byte splice mismatch:\ngot:\n%s\nwant:\n%s", patched, want)
	}

	// Missing path must return ErrNotExist.
	if _, err := PatchByPath(orig, PathPatch{Path: []interface{}{"meta", "nonexistent"}, Value: 1}); !errors.Is(err, ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}

	// Duplicate path must return ErrPatchConflict.
	if _, err := PatchByPath(orig,
		PathPatch{Path: []interface{}{"id"}, Value: 1},
		PathPatch{Path: []interface{}{"id"}, Value: 2},
	); !errors.Is(err, ErrPatchConflict) {
		t.Fatalf("expected ErrPatchConflict on duplicate path, got %v", err)
	}

	// Ancestor/descendant overlap must return ErrPatchConflict.
	if _, err := PatchByPath(orig,
		PathPatch{Path: []interface{}{"meta"}, Value: map[string]int{"x": 1}},
		PathPatch{Path: []interface{}{"meta", "tag"}, Value: "y"},
	); !errors.Is(err, ErrPatchConflict) {
		t.Fatalf("expected ErrPatchConflict on ancestor/descendant overlap, got %v", err)
	}

	// Invalid raw JSON must return ErrInvalidPatchRaw.
	if _, err := PatchByPath(orig,
		PathPatch{Path: []interface{}{"id"}, Raw: true, RawValue: []byte(`{bad`)},
	); !errors.Is(err, ErrInvalidPatchRaw) {
		t.Fatalf("expected ErrInvalidPatchRaw, got %v", err)
	}

	// Unencodable replacement (NaN) must fail cleanly.
	if _, err := PatchByPath(orig,
		PathPatch{Path: []interface{}{"id"}, Value: math.NaN()},
	); err == nil {
		t.Fatal("expected NaN encoding error")
	}
}

var benchTwitter10Paths = [][]interface{}{
	{"statuses", 0, "id_str"},
	{"statuses", 0, "text"},
	{"statuses", 0, "user", "screen_name"},
	{"statuses", 0, "user", "followers_count"},
	{"statuses", 1, "id_str"},
	{"statuses", 1, "user", "screen_name"},
	{"statuses", 2, "id_str"},
	{"statuses", 3, "id_str"},
	{"search_metadata", "count"},
	{"search_metadata", "max_id_str"},
}

func BenchmarkGetByPathSequential10_Twitter(b *testing.B) {
	b.SetBytes(int64(len(_TwitterJson)))
	b.ReportAllocs()
	s := NewSearcher(_TwitterJson)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, p := range benchTwitter10Paths {
			if _, err := s.GetByPath(p...); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkSearchMany10_Twitter(b *testing.B) {
	b.SetBytes(int64(len(_TwitterJson)))
	b.ReportAllocs()
	plan := MustCompilePathPlan(benchTwitter10Paths...)
	s := NewSearcher(_TwitterJson)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodes, err := s.GetManyWithPlan(plan)
		if err != nil || len(nodes) != 10 {
			b.Fatal(err)
		}
	}
}

func BenchmarkPatchByPath2_Twitter(b *testing.B) {
	src := []byte(_TwitterJson)
	plan := MustCompilePathPlan(
		[]interface{}{"statuses", 0, "text"},
		[]interface{}{"search_metadata", "count"},
	)
	patches := []PathPatch{
		{Path: []interface{}{"statuses", 0, "text"}, Value: "patched tweet text for forwarding"},
		{Path: []interface{}{"search_metadata", "count"}, Value: 99},
	}
	// Verify result is valid JSON once before timing.
	out, err := PatchWithPlan(src, plan, patches...)
	if err != nil || !json.Valid(out) {
		b.Fatalf("invalid patched output: %v", err)
	}
	b.SetBytes(int64(len(src)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		res, err := PatchWithPlan(src, plan, patches...)
		if err != nil || len(res) == 0 {
			b.Fatal(err)
		}
	}
}

func BenchmarkASTParseSetMarshal2_Twitter(b *testing.B) {
	b.SetBytes(int64(len(_TwitterJson)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root, perr := NewParser(_TwitterJson).Parse()
		if perr != 0 {
			b.Fatal(perr)
		}
		if _, err := root.GetByPath("statuses", 0).Set("text", NewString("patched tweet text for forwarding")); err != nil {
			b.Fatal(err)
		}
		if _, err := root.Get("search_metadata").Set("count", NewNumber("99")); err != nil {
			b.Fatal(err)
		}
		res, err := root.MarshalJSON()
		if err != nil || len(res) == 0 {
			b.Fatal(err)
		}
	}
}

func TestB01_A01_A02_EdgeContracts(t *testing.T) {
	// A01: First-key rule — duplicate later key must NOT backfill missing nested child.
	dupJSON := `{"a":{},"a":{"b":1},"c":2}`
	nodes, err := SearchMany(dupJSON, SearchOptions{ValidateJSON: true},
		[]interface{}{"a", "b"},
		[]interface{}{"c"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nodes[0].Exists() {
		t.Fatalf("expected a.b to be missing due to first-key rule, got %v", nodes[0])
	}
	if v, err := nodes[1].Int64(); err != nil || v != 2 {
		t.Fatalf("expected c == 2, got %v, %v", v, err)
	}

	// A01: Parent + child co-selection & empty root path & duplicate path independence.
	coJSON := `{"user":{"id":7,"tags":["x","y"]}}`
	coNodes, err := SearchMany(coJSON, SearchOptions{ValidateJSON: true},
		[]interface{}{},
		[]interface{}{"user"},
		[]interface{}{"user", "id"},
		[]interface{}{"user"},
	)
	if err != nil {
		t.Fatalf("co-selection error: %v", err)
	}
	if r0, _ := coNodes[0].Raw(); r0 != coJSON {
		t.Fatalf("root path mismatch: %s", r0)
	}
	if r1, _ := coNodes[1].Raw(); r1 != `{"id":7,"tags":["x","y"]}` {
		t.Fatalf("parent path mismatch: %s", r1)
	}
	if id, _ := coNodes[2].Int64(); id != 7 {
		t.Fatalf("child path mismatch: %d", id)
	}
	// Mutating coNodes[1] must not mutate coNodes[3].
	if _, err := coNodes[1].Set("id", NewNumber("999")); err != nil {
		t.Fatalf("Set on coNodes[1] failed: %v", err)
	}
	if id3, _ := coNodes[3].Get("id").Int64(); id3 != 7 {
		t.Fatalf("duplicate requested Node shared mutable state: got %d", id3)
	}

	// A02: PatchByPath must validate full single-root JSON and reject trailing garbage,
	// truncated root JSON, or syntax errors in unvisited sibling branches.
	if _, err := PatchByPath([]byte(`{"a":1`), PathPatch{Path: []interface{}{"a"}, Value: 2}); err == nil {
		t.Fatal("expected PatchByPath to reject truncated root JSON")
	}
	if _, err := PatchByPath([]byte(`{"a":1} trailing`), PathPatch{Path: []interface{}{"a"}, Value: 2}); err == nil {
		t.Fatal("expected PatchByPath to reject trailing garbage")
	}
	if _, err := PatchByPath([]byte(`{"a":1,"bad":[1,}`), PathPatch{Path: []interface{}{"a"}, Value: 2}); err == nil {
		t.Fatal("expected PatchByPath to reject syntax error in unvisited sibling")
	}
	emptyPlan := MustCompilePathPlan()
	if _, err := PatchWithPlan([]byte(`{"a":1} extra`), emptyPlan); err == nil {
		t.Fatal("expected PatchWithPlan with empty plan to reject trailing garbage")
	}

	// [P2] Querying an ancestor and a descendant simultaneously must preserve full
	// JSON validation of the ancestor subtree, reject trailing commas in objects,
	// and enforce cumulative recursion depth limits.
	if _, err := SearchMany(
		`{"a":{"b":1,"x":truX}}`,
		SearchOptions{ValidateJSON: true},
		[]interface{}{},
		[]interface{}{"a", "b"},
	); err == nil {
		t.Fatal("expected SearchMany on ancestor+descendant to reject invalid sibling truX inside intermediate object")
	}
	if _, err := SearchMany(
		`{"a":[1,truX]}`,
		SearchOptions{ValidateJSON: true},
		[]interface{}{},
		[]interface{}{"a", 0},
	); err == nil {
		t.Fatal("expected SearchMany on ancestor+descendant to reject invalid sibling truX inside intermediate array")
	}
	if _, err := SearchMany(
		`{"a":1,}`,
		SearchOptions{ValidateJSON: true},
		[]interface{}{},
		[]interface{}{"a"},
	); err == nil {
		t.Fatal("expected SearchMany to reject trailing comma in object when querying root and 'a'")
	}
	if _, err := SearchMany(
		`{"a":1,}`,
		SearchOptions{ValidateJSON: false},
		[]interface{}{"b"},
	); err == nil {
		t.Fatal("expected SearchMany to reject trailing comma in object when scanning past 'a'")
	}
	deepTooMany := strings.Repeat("[", 4097) + "0" + strings.Repeat("]", 4097)
	if _, err := SearchMany(
		deepTooMany,
		SearchOptions{ValidateJSON: true},
		[]interface{}{0},
		[]interface{}{0, 0},
	); err == nil {
		t.Fatal("expected SearchMany to enforce cumulative MAX_RECURSE across branch descent and target validation")
	}
}
