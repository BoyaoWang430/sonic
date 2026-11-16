package sonic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/rt"
)

type mutatingUnmarshaler struct {
	copied string
}

func (m *mutatingUnmarshaler) UnmarshalJSON(b []byte) error {
	m.copied = string(b)
	if len(b) > 0 {
		b[0] = '!' // must be writable heap memory, never .rodata or caller string
	}
	return nil
}

type definedRawFromNoCopy NoCopyRawMessage

func (d *definedRawFromNoCopy) UnmarshalJSON(b []byte) error {
	if len(b) > 0 {
		b[0] = '?'
	}
	*d = append((*d)[:0], b...)
	return nil
}

type aliasNoCopyRaw = NoCopyRawMessage

type strJSONUnmarshaler string

func (u *strJSONUnmarshaler) UnmarshalJSON(b []byte) error {
	if len(b) > 0 {
		orig := b[0]
		b[0] = '@' // verify strOpt buffer is also writable
		b[0] = orig
	}
	*u = strJSONUnmarshaler(b)
	return nil
}

func TestUnmarshalJSONWritableAndNoCopyBoundaries(t *testing.T) {
	tokens := []string{"null", "true", "false", `"hello"`, "12345", `{"a":1}`, `[1,2]`}
	for _, tok := range tokens {
		srcCopy := string([]byte(tok))
		for _, cfg := range []API{ConfigDefault, ConfigStd} {
			var m mutatingUnmarshaler
			if err := cfg.UnmarshalFromString(srcCopy, &m); err != nil {
				t.Fatalf("tok=%s err=%v", tok, err)
			}
			if m.copied != tok || srcCopy != tok {
				t.Fatalf("tok=%s mutated source=%q copied=%q", tok, srcCopy, m.copied)
			}
			var def definedRawFromNoCopy
			if err := cfg.UnmarshalFromString(srcCopy, &def); err != nil {
				t.Fatalf("defined type tok=%s err=%v", tok, err)
			}
			if srcCopy != tok {
				t.Fatalf("defined type mutated source: %q", srcCopy)
			}
		}

		// Exact NoCopyRawMessage & alias with CopyString=false borrows from source string
		var nc NoCopyRawMessage
		if err := ConfigDefault.UnmarshalFromString(srcCopy, &nc); err != nil {
			t.Fatal(err)
		}
		if string(nc) != tok {
			t.Fatalf("nocopy got %q want %q", nc, tok)
		}
		sPtr := uintptr(rt.IndexChar(srcCopy, 0))
		ncPtr := uintptr(unsafe.Pointer(rt.SliceData(nc)))
		if ncPtr < sPtr || ncPtr >= sPtr+uintptr(len(srcCopy)) {
			t.Fatalf("tok=%s expected zero-copy slice into source [%#x..), got %#x", tok, sPtr, ncPtr)
		}

		// Non-empty interface json.Unmarshaler holding *NoCopyRawMessage
		if tok != "null" {
			var ncIface NoCopyRawMessage
			var u json.Unmarshaler = &ncIface
			if err := ConfigDefault.UnmarshalFromString(srcCopy, &u); err != nil {
				t.Fatal(err)
			}
			if uintptr(unsafe.Pointer(rt.SliceData(ncIface))) != sPtr {
				t.Fatalf("interface dispatch lost NoCopyRawMessage fast path for %s", tok)
			}
		}

		// CopyString=true MUST copy even for NoCopyRawMessage
		inBuf := []byte(tok)
		var ncStd aliasNoCopyRaw
		if err := ConfigStd.Unmarshal(inBuf, &ncStd); err != nil {
			t.Fatal(err)
		}
		for i := range inBuf {
			inBuf[i] = '#'
		}
		if string(ncStd) != tok {
			t.Fatalf("CopyString=true NoCopyRawMessage aliased caller []byte: got %q want %q", ncStd, tok)
		}
	}

	// Pointer *NoCopyRawMessage on null -> nil; ,string -> unquoted
	type edgeHolder struct {
		Ptr *NoCopyRawMessage  `json:"ptr"`
		Val NoCopyRawMessage   `json:"val"`
		Str strJSONUnmarshaler `json:"str,string"`
	}
	initRaw := NoCopyRawMessage("old")
	eh := edgeHolder{Ptr: &initRaw}
	if err := ConfigDefault.UnmarshalFromString(`{"ptr":null,"val":null,"str":"\"inner\""}`, &eh); err != nil {
		t.Fatal(err)
	}
	if eh.Ptr != nil || string(eh.Val) != "null" || string(eh.Str) != `"inner"` {
		t.Fatalf("unexpected edgeHolder: ptr=%v val=%q str=%q", eh.Ptr, eh.Val, eh.Str)
	}
}

func TestStructInterfaceTypedNilNull(t *testing.T) {
	type box struct {
		F any `json:"f"`
	}
	cases := []func() box{
		func() box { return box{F: nil} },
		func() box { return box{F: (*int)(nil)} },
		func() box { return box{F: (**int)(nil)} },
		func() box {
			v := 7
			p := &v
			return box{F: &p}
		},
	}
	for idx, mk := range cases {
		sGot, sWant := mk(), mk()
		if err := ConfigDefault.Unmarshal([]byte(`{"f":null}`), &sGot); err != nil {
			t.Fatalf("case %d sonic err: %v", idx, err)
		}
		if err := json.Unmarshal([]byte(`{"f":null}`), &sWant); err != nil {
			t.Fatalf("case %d std err: %v", idx, err)
		}
		if !reflect.DeepEqual(sGot, sWant) || (sWant.F == nil && sGot.F != nil) {
			t.Fatalf("case %d mismatch: got=%#v (nil=%v) want=%#v (nil=%v)", idx, sGot.F, sGot.F == nil, sWant.F, sWant.F == nil)
		}
	}
}

func TestStructWideFieldOffsetOver4GiB(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) < 8 {
		t.Skip("requires 64-bit address space")
	}
	type wideStruct struct {
		Head    int            `json:"head"`
		_       [1 << 32]uint8 // 4 GiB demand-paged virtual gap
		Tail    int            `json:"tail"`
		TailPtr *int           `json:"tail_ptr"`
	}
	w := new(wideStruct)
	w.Head = 11
	v := 88
	w.TailPtr = &v
	if err := ConfigDefault.Unmarshal([]byte(`{"tail":42,"tail_ptr":null}`), w); err != nil {
		t.Fatal(err)
	}
	if w.Head != 11 || w.Tail != 42 || w.TailPtr != nil {
		t.Fatalf("wide offset corruption: head=%d tail=%d tailPtr=%v", w.Head, w.Tail, w.TailPtr)
	}
}

func TestEncodeIntoPrefixPreserved(t *testing.T) {
	dst := []byte(`"prefix<keep>&":`)
	err := encoder.EncodeInto(&dst, map[string]string{"k": "<v>"}, encoder.EscapeHTML|encoder.ValidateString)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`"prefix<keep>&":{"k":"\u003cv\u003e"}`)
	if !bytes.Equal(dst, want) {
		t.Fatalf("EncodeInto prefix altered:\ngot:  %s\nwant: %s", dst, want)
	}
}

func TestMultiValueDecoderSessionReuse(t *testing.T) {
	const N = 200
	var sb strings.Builder
	for i := 0; i < N; i++ {
		fmt.Fprintf(&sb, "{\"id\":%d,\"msg\":\"line\\n_%d\"}\n", i, i)
	}
	stream := sb.String()
	dec := decoder.NewDecoder(stream)
	dec.ValidateString()
	dec.CopyString()

	type rec struct {
		ID  int    `json:"id"`
		Msg string `json:"msg"`
	}
	for i := 0; i < N; i++ {
		var r rec
		if err := dec.Decode(&r); err != nil {
			t.Fatalf("rec %d err: %v", i, err)
		}
		if r.ID != i || r.Msg != fmt.Sprintf("line\n_%d", i) {
			t.Fatalf("rec %d mismatch: %+v", i, r)
		}
	}
	if err := dec.CheckTrailings(); err != nil {
		t.Fatalf("trailing check failed: %v", err)
	}
}

func BenchmarkMultiValueDecoder1000(b *testing.B) {
	const N = 1000
	var sb strings.Builder
	for i := 0; i < N; i++ {
		fmt.Fprintf(&sb, "{\"id\":%d,\"msg\":\"item\\n_%d\",\"ok\":true}\n", i, i)
	}
	stream := sb.String()
	type rec struct {
		ID  int    `json:"id"`
		Msg string `json:"msg"`
		OK  bool   `json:"ok"`
	}
	b.SetBytes(int64(len(stream)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dec := decoder.NewDecoder(stream)
		dec.ValidateString()
		var r rec
		for j := 0; j < N; j++ {
			if err := dec.Decode(&r); err != nil {
				b.Fatal(err)
			}
		}
	}
}
