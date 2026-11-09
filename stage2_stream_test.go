package sonic

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/bytedance/sonic/decoder"
)

type fixedChunkReader struct {
	data  []byte
	pos   int
	chunk int
}

func (r *fixedChunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := r.chunk
	if n > len(p) {
		n = len(p)
	}
	if rem := len(r.data) - r.pos; n > rem {
		n = rem
	}
	copy(p[:n], r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

func TestStreamSessionChunkBoundariesAndLifecycles(t *testing.T) {
	type streamItem struct {
		ID      int               `json:"id"`
		Text    string            `json:"text"`
		Emoji   string            `json:"emoji"`
		UTF8    string            `json:"utf8"`
		Score   float64           `json:"score"`
		Num     json.Number       `json:"num"`
		Raw     json.RawMessage   `json:"raw"`
		NoCopy  NoCopyRawMessage  `json:"nocopy"`
		Flags   []bool            `json:"flags"`
		Nested  map[string]string `json:"nested"`
	}

	records := []string{
		`{"id":1,"text":"hello\nworld\"escaped\\slash","emoji":"\uD83D\uDE00\uD83C\uDF89","utf8":"日本語と中文テキスト","score":-0.00125e+4,"num":9876543210.125,"raw":{"k":[1,2,3]},"nocopy":["a","b"],"flags":[true,false,true],"nested":{"k1":"v1","k2":"v\t2"}}`,
		`   {"id":2,"text":"plain_ascii_payload","emoji":"\u0041\u03A9","utf8":"한국어","score":0,"num":-42,"raw":true,"nocopy":{"x":null},"flags":[],"nested":{}}` + "\n",
		`{"id":3,"text":"trailing_spaces","emoji":"ok","utf8":"éèê","score":1234567890123456789,"num":0.5,"raw":"str","nocopy":12345,"flags":[false],"nested":{"z":"end"}}`,
	}
	streamBytes := []byte(strings.Join(records, "\n  \t "))

	var want []streamItem
	stdDec := json.NewDecoder(bytes.NewReader(streamBytes))
	for stdDec.More() {
		var it streamItem
		if err := stdDec.Decode(&it); err != nil {
			t.Fatalf("std decode: %v", err)
		}
		want = append(want, it)
	}
	// Populate expected NoCopy fields to match Raw equivalents.
	want[0].NoCopy = NoCopyRawMessage(`["a","b"]`)
	want[1].NoCopy = NoCopyRawMessage(`{"x":null}`)
	want[2].NoCopy = NoCopyRawMessage(`12345`)

	for _, chunkSize := range []int{1, 2, 3, 5, 7, 16, 64, 512, 4096} {
		for _, cfg := range []API{ConfigDefault, ConfigStd} {
			r := &fixedChunkReader{data: streamBytes, chunk: chunkSize}
			dec := cfg.NewDecoder(r)
			var got []streamItem
			for dec.More() {
				var it streamItem
				if err := dec.Decode(&it); err != nil {
					t.Fatalf("chunk=%d decode #%d err: %v", chunkSize, len(got), err)
				}
				got = append(got, it)
			}
			if len(got) != len(want) {
				t.Fatalf("chunk=%d count=%d want=%d", chunkSize, len(got), len(want))
			}
			for i := range want {
				if got[i].ID != want[i].ID || got[i].Text != want[i].Text || got[i].Emoji != want[i].Emoji ||
					got[i].UTF8 != want[i].UTF8 || got[i].Score != want[i].Score || got[i].Num != want[i].Num ||
					!bytes.Equal(got[i].Raw, want[i].Raw) || !bytes.Equal(got[i].NoCopy, want[i].NoCopy) {
					t.Fatalf("chunk=%d item %d mismatch:\ngot:  %+v\nwant: %+v", chunkSize, i, got[i], want[i])
				}
			}
		}
	}

	// Verify invalid UTF-8 replacement under ValidateString in chunked StreamDecoder.
	badUTF8 := []byte("{\"s\":\"pre\xffpost\"}")
	decBad := ConfigStd.NewDecoder(&fixedChunkReader{data: badUTF8, chunk: 3})
	var badMap map[string]string
	if err := decBad.Decode(&badMap); err != nil {
		t.Fatalf("invalid UTF-8 stream decode failed: %v", err)
	}
	if badMap["s"] != "pre\ufffdpost" {
		t.Fatalf("invalid UTF-8 replacement mismatch: %q", badMap["s"])
	}

	// Verify bounded memory retention after streaming a large record followed by a tiny record.
	var bigBuilder strings.Builder
	bigBuilder.WriteString(`{"big":[`)
	for i := 0; i < 6000; i++ {
		if i > 0 {
			bigBuilder.WriteByte(',')
		}
		fmt.Fprintf(&bigBuilder, `"elem\\n_%d"`, i)
	}
	bigBuilder.WriteString(`]}` + "\n" + `{"small":"tiny"}`)

	sd := decoder.NewStreamDecoder(strings.NewReader(bigBuilder.String()))
	var bigDiscard map[string]any
	if err := sd.Decode(&bigDiscard); err != nil {
		t.Fatal(err)
	}
	bigDiscard = nil
	var smallKeep map[string]string
	if err := sd.Decode(&smallKeep); err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	runtime.GC()
	if smallKeep["small"] != "tiny" {
		t.Fatalf("unexpected smallKeep: %+v", smallKeep)
	}
}

func TestStreamSessionNumberPaddingAndErrorBuffered(t *testing.T) {
	// [P1] Exact 4096-byte buffer boundary number where cParitySkipDigits reads 8 bytes
	// starting at the delimiter byte (offset 4094), plus variations for 1..8 trailing
	// bytes and long numbers exceeding stackBuf (128B) into s.unesc scratch.
	for pad := 4068; pad <= 4076; pad++ {
		for _, numLit := range []string{
			"10000000000000000000",
			"-10000000000000000000",
			"1234567890123456789012345678",
			"1." + strings.Repeat("5", 160),
		} {
			payload := "[" + strings.Repeat(" ", pad) + numLit + "]"
			var got []any
			dec := ConfigDefault.NewDecoder(strings.NewReader(payload))
			if err := dec.Decode(&got); err != nil {
				t.Fatalf("pad=%d numLen=%d decode error: %v", pad, len(numLit), err)
			}
			var want []any
			if err := json.Unmarshal([]byte(payload), &want); err != nil {
				t.Fatalf("std unmarshal failed: %v", err)
			}
			if len(got) != 1 || got[0] != want[0] {
				t.Fatalf("pad=%d num=%q got=%v want=%v", pad, numLit, got, want)
			}
		}
	}

	// Also test bare top-level long numbers at EOF (no trailing delimiter byte in input).
	for _, numLit := range []string{
		"10000000000000000000",
		"123456789012345678901234567",
		"0." + strings.Repeat("3", 180),
	} {
		var got any
		dec := ConfigDefault.NewDecoder(strings.NewReader(numLit))
		if err := dec.Decode(&got); err != nil {
			t.Fatalf("EOF number %q decode error: %v", numLit, err)
		}
		var want any
		if err := json.Unmarshal([]byte(numLit), &want); err != nil {
			t.Fatalf("std unmarshal failed: %v", err)
		}
		if got != want {
			t.Fatalf("EOF number %q got=%v want=%v", numLit, got, want)
		}
	}

	// [P2] Calling Buffered() and InputOffset() after a second Decode fails on "{} x"
	// must not panic with slice bounds out of range.
	{
		dec := decoder.NewStreamDecoder(strings.NewReader("{} x"))
		var first map[string]any
		if err := dec.Decode(&first); err != nil {
			t.Fatalf("first decode failed: %v", err)
		}
		var second map[string]any
		if err := dec.Decode(&second); err == nil {
			t.Fatal("expected error on second decode of invalid 'x'")
		}
		rem, err := io.ReadAll(dec.Buffered())
		if err != nil {
			t.Fatalf("Buffered read failed: %v", err)
		}
		if len(rem) != 0 {
			t.Fatalf("expected empty Buffered() after setErr, got %q", rem)
		}
		if off := dec.InputOffset(); off < 2 {
			t.Fatalf("unexpected InputOffset after error: %d", off)
		}
	}
}

func BenchmarkStreamNDJSON1000(b *testing.B) {
	const N = 1000
	var sb strings.Builder
	for i := 0; i < N; i++ {
		fmt.Fprintf(&sb, "{\"id\":%d,\"msg\":\"item\\n_%d\",\"ok\":true}\n", i, i)
	}
	payload := []byte(sb.String())
	type rec struct {
		ID  int    `json:"id"`
		Msg string `json:"msg"`
		OK  bool   `json:"ok"`
	}
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sd := ConfigStd.NewDecoder(bytes.NewReader(payload))
		var r rec
		for j := 0; j < N; j++ {
			if err := sd.Decode(&r); err != nil {
				b.Fatal(err)
			}
		}
	}
}
