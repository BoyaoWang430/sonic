package encoder

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/bytedance/sonic/internal/resolver"
)

func TestKeyPlanSeparatedFromDecoderResolverAndSliceVariants(t *testing.T) {
	type decoderOnlyStruct struct {
		Alpha int    `json:"alpha"`
		Beta  string `json:"beta"`
	}
	vt := reflect.TypeOf(decoderOnlyStruct{})
	fms := resolver.ResolveStruct(vt)
	if len(fms) != 2 {
		t.Fatalf("expected 2 resolved fields, got %d", len(fms))
	}

	keyPlanLock.RLock()
	_, populatedByResolver := keyPlanCache[vt]
	keyPlanLock.RUnlock()
	if populatedByResolver {
		t.Fatalf("resolver.ResolveStruct must not populate encoder keyPlanCache")
	}

	type child struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type namedChildren []child
	type namedPtrChildren []*child
	type holder struct {
		NilVal   namedChildren    `json:"nil_val"`
		EmptyVal namedChildren    `json:"empty_val"`
		Items    namedChildren    `json:"items"`
		NilPtr   namedPtrChildren `json:"nil_ptr"`
		EmptyPtr namedPtrChildren `json:"empty_ptr"`
		Ptrs     namedPtrChildren `json:"ptrs"`
		OmitNil  namedChildren    `json:"omit_nil,omitempty"`
		OmitEmp  namedChildren    `json:"omit_emp,omitempty"`
	}

	c1 := child{ID: 1, Name: "one"}
	c2 := child{ID: 2, Name: "two"}
	in := holder{
		NilVal:   nil,
		EmptyVal: namedChildren{},
		Items:    namedChildren{c1, c2},
		NilPtr:   nil,
		EmptyPtr: namedPtrChildren{},
		Ptrs:     namedPtrChildren{&c1, nil, &c2},
		OmitNil:  nil,
		OmitEmp:  namedChildren{},
	}

	out, err := Encode(in, 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	out2, err := Encode(&in, EscapeHTML)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"nil_val":null,"empty_val":[],"items":[{"id":1,"name":"one"},{"id":2,"name":"two"}],"nil_ptr":null,"empty_ptr":[],"ptrs":[{"id":1,"name":"one"},null,{"id":2,"name":"two"}]}`
	if string(out) != want || string(out2) != want {
		t.Fatalf("got:\n  %s\n  %s\nwant:\n  %s", out, out2, want)
	}

	outNoNull, err := Encode(in, NoNullSliceOrMap)
	if err != nil {
		t.Fatal(err)
	}
	const wantNoNull = `{"nil_val":[],"empty_val":[],"items":[{"id":1,"name":"one"},{"id":2,"name":"two"}],"nil_ptr":[],"empty_ptr":[],"ptrs":[{"id":1,"name":"one"},null,{"id":2,"name":"two"}]}`
	if string(outNoNull) != wantNoNull {
		t.Fatalf("NoNullSliceOrMap got:\n  %s\nwant:\n  %s", outNoNull, wantNoNull)
	}
}
