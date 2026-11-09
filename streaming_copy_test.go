package sonic

import (
	"bytes"
	"encoding/json"
	"testing"
)

type dirtyKeyAndMarshalerPayload struct {
	Normal   string            `json:"normal"`
	Quoted   string            `json:"quoted,string"`
	DirtyKey string            `json:"<dirty>&key"`
	Items    []string          `json:"items"`
	Map      map[string]string `json:"map"`
	AnyMap   map[string]any    `json:"any_map"`
	Raw      json.RawMessage   `json:"raw"`
}

func TestStreamingEscapeHTMLAndValidateStringParity(t *testing.T) {
	v := dirtyKeyAndMarshalerPayload{
		Normal:   "<a href=\"https://example.com?a=1&b=2\">link\u2028\u2029</a>",
		Quoted:   "hello_quoted",
		DirtyKey: "ok<val>",
		Items:    []string{"plain_ascii_url_https://example.com/1234567890", "<b>bold</b>", "日本語テキスト"},
		Map:      map[string]string{"<k1>": "v&1", "k2": "clean"},
		AnyMap:   map[string]any{"<a>": "<v>", "num": 42},
		Raw:      json.RawMessage(`{"inner":"<tag>&\u2028"}`),
	}
	got, err := ConfigStd.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("ConfigStd.Marshal mismatch with stdlib:\ngot:  %s\nwant: %s", got, want)
	}

	// Test invalid UTF-8 replacement (\ufffd) + EscapeHTML
	vBad := dirtyKeyAndMarshalerPayload{
		Normal:   "bad\xffutf8<tag>",
		Quoted:   "q\xff",
		DirtyKey: "d\xff",
		Items:    []string{"x\xe2\x80y"},
	}
	gotBad, err := ConfigStd.Marshal(vBad)
	if err != nil {
		t.Fatal(err)
	}
	wantBad := []byte(`{"normal":"bad\ufffdutf8\u003ctag\u003e","quoted":"\"q\ufffd\"","\u003cdirty\u003e\u0026key":"d\ufffd","items":["x\ufffd\ufffdy"],"map":null,"any_map":null,"raw":null}`)
	if !bytes.Equal(gotBad, wantBad) {
		t.Fatalf("invalid UTF-8 Marshal mismatch:\ngot:  %s\nwant: %s", gotBad, wantBad)
	}
}

func TestUnmarshalCopyStringMutateInputBuffer(t *testing.T) {
	type payload struct {
		Name   string                 `json:"name"`
		Num    json.Number            `json:"num"`
		Raw    json.RawMessage        `json:"raw"`
		NoCopy NoCopyRawMessage       `json:"nocopy"`
		Map    map[string]string      `json:"map"`
		NumMap map[json.Number]string `json:"num_map"`
	}
	src := `{"name":"alice","num":12345.67,"raw":{"x":1},"nocopy":[1,2,3],"map":{"k":"v"},"num_map":{"99":"ninety"}}`
	buf := []byte(src)

	var dst payload
	if err := ConfigStd.Unmarshal(buf, &dst); err != nil {
		t.Fatal(err)
	}
	var gen any
	cfgNum := Config{CopyString: true, UseNumber: true, ValidateString: true}.Froze()
	buf2 := []byte(src)
	if err := cfgNum.Unmarshal(buf2, &gen); err != nil {
		t.Fatal(err)
	}

	// Overwrite input buffers completely to prove zero retained references
	for i := range buf {
		buf[i] = 'X'
		buf2[i] = 'Y'
	}

	if dst.Name != "alice" || string(dst.Num) != "12345.67" || string(dst.Raw) != `{"x":1}` ||
		string(dst.NoCopy) != `[1,2,3]` || dst.Map["k"] != "v" || dst.NumMap[json.Number("99")] != "ninety" {
		t.Fatalf("mutated input buffer corrupted struct result: %+v", dst)
	}
	gm := gen.(map[string]any)
	if gm["name"].(string) != "alice" || string(gm["num"].(json.Number)) != "12345.67" {
		t.Fatalf("mutated input buffer corrupted generic result: %+v", gm)
	}

	// Verify syntax error Src also does not alias mutated input buffer
	badBuf := []byte(`{"name": truX}`)
	err := ConfigStd.Unmarshal(badBuf, &dst)
	if err == nil {
		t.Fatal("expected syntax error")
	}
	msgBefore := err.Error()
	for i := range badBuf {
		badBuf[i] = 'Z'
	}
	if err.Error() != msgBefore {
		t.Fatalf("syntax error message mutated after overwriting input buffer:\nbefore: %s\nafter:  %s", msgBefore, err.Error())
	}
}
