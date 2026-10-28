//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package optdec

import (
	"encoding/json"
	"reflect"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

type rvvBenchDocument struct {
	ID      int64     `json:"id"`
	Text    string    `json:"text"`
	Numbers []float64 `json:"numbers"`
	Enabled bool      `json:"enabled"`
}

// Stage measurements use the same typed destination and default options.
// Compilation is warmed outside timing. Stages are not additive: ParseBatch
// reuses resident storage, FromDom reuses a parsed tape, and Total includes the
// production pool/context lifecycle and destination allocations.
func BenchmarkRVVDecodeStages(b *testing.B) {
	b.Run("SmallPlain", func(b *testing.B) {
		benchmarkRVVDecodeStages[rvvBenchDocument](b,
			`{"id":12345,"text":"hello JSON world","numbers":[1,2.5,-3e2],"enabled":true}`)
	})
	b.Run("SmallEscapedUnicode", func(b *testing.B) {
		benchmarkRVVDecodeStages[rvvBenchDocument](b,
			`{"id":12345,"text":"hello\n\u4e2d\u6587\ud83d\ude00","numbers":[1,2.5,-3e2],"enabled":true}`)
	})
	b.Run("Twitter", func(b *testing.B) {
		benchmarkRVVDecodeStages[TwitterStruct](b, TwitterJson)
	})
}

func benchmarkRVVDecodeStages[T any](b *testing.B, src string) {
	b.Helper()
	var want, warm T
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		b.Fatal(err)
	}
	input, pos := src, 0
	if err := Decode(&input, &pos, 0, &warm); err != nil || pos != len(src) || !reflect.DeepEqual(warm, want) {
		b.Fatalf("fixture decode failed at %d: %v", pos, err)
	}
	typ := rt.UnpackType(reflect.TypeOf((*T)(nil)).Elem())
	dec, err := findOrCompile(typ)
	if err != nil {
		b.Fatal(err)
	}
	checkParser := func(b *testing.B, p *Parser) {
		b.Helper()
		if p.Pos() != len(src) {
			b.Fatalf("parser stopped at %d of %d", p.Pos(), len(src))
		}
		ctx := Context{Parser: p}
		var got T
		if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil || !reflect.DeepEqual(got, want) {
			b.Fatalf("parsed tape materialization mismatch: %v", err)
		}
	}

	b.Run("Prepare", func(b *testing.B) {
		// Includes pool get/put, the input copy and reset.
		// No UTF-8 correction pass is requested by default options.
		p := newParser(src, 0, 0)
		if p.Json != src || string(p.padded) != src || p.Pos() != 0 {
			p.free()
			b.Fatal("incorrect prepared input")
		}
		p.free()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := newParser(src, 0, 0)
			p.free()
		}
		b.StopTimer()
	})

	b.Run("PrepareAndParse", func(b *testing.B) {
		p := newParser(src, 0, 0)
		if code := p.parse(); code != SONIC_OK {
			b.Fatal(code)
		}
		checkParser(b, p)
		p.free()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := newParser(src, 0, 0)
			code, pos := p.parse(), p.Pos()
			p.free()
			if code != SONIC_OK || pos != len(src) {
				b.Fatalf("parse code=%d pos=%d", code, pos)
			}
		}
		b.StopTimer()
	})

	b.Run("ParseBatch", func(b *testing.B) {
		// Parsing may mutate strings. Restore original bytes, node statistics
		// and cursors outside timing, once per batch of up to 32 documents.
		// Timed work includes parser-owned allocations, but excludes pooled
		// input preparation and these cache-warming restoration copies.
		batch := min(32, b.N)
		parsers := make([]*Parser, batch)
		codes := make([]ErrorCode, batch)
		for i := range parsers {
			parsers[i] = newParser(src, 0, 0)
		}
		defer func() {
			for _, p := range parsers {
				p.free()
			}
		}()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		b.StopTimer()
		for done := 0; done < b.N; {
			n := min(batch, b.N-done)
			for _, p := range parsers[:n] {
				copy(p.padded, src)
				p.cur = p.start
				p.nbuf = nodeBuf{}
				p.nbuf.init(p.nodes)
			}
			b.StartTimer()
			for i, p := range parsers[:n] {
				codes[i] = p.parse()
			}
			b.StopTimer()
			for i, p := range parsers[:n] {
				if codes[i] != SONIC_OK || p.Pos() != len(src) {
					b.Fatalf("parse code=%d pos=%d", codes[i], p.Pos())
				}
			}
			done += n
		}
		for _, p := range parsers {
			checkParser(b, p)
		}
	})

	b.Run("FromDom", func(b *testing.B) {
		ctx, err := NewContext(src, 0, 0, typ)
		defer ctx.Delete()
		if err != nil {
			b.Fatal(err)
		}
		// A bound struct avoids the one-shot interface arenas. Nested
		// interface fields use the normal allocation path in this context.
		if ctx.efacePool != nil {
			b.Fatal("FromDom fixture unexpectedly uses an interface arena")
		}
		checkParser(b, ctx.Parser)
		root := ctx.Root()
		var got, zero T
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got = zero
			if err := dec.FromDom(unsafe.Pointer(&got), root, &ctx); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		if !reflect.DeepEqual(got, want) || ctx.Stack.index != 0 {
			b.Fatal("repeated FromDom changed output or left stack state")
		}
	})

	b.Run("Total", func(b *testing.B) {
		var got, zero T
		input, pos := src, 0
		if err := Decode(&input, &pos, 0, &got); err != nil || pos != len(src) || !reflect.DeepEqual(got, want) {
			b.Fatalf("warm decode stopped at %d: %v", pos, err)
		}
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			got = zero
			input, pos := src, 0
			if err := Decode(&input, &pos, 0, &got); err != nil || pos != len(src) {
				b.Fatalf("decode stopped at %d: %v", pos, err)
			}
		}
		b.StopTimer()
		if !reflect.DeepEqual(got, want) {
			b.Fatal("total decode differs from the standard library")
		}
	})
}
