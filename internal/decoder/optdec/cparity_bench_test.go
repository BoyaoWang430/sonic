//go:build arm64 && go1.20 && !go1.28

package optdec

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"unsafe"

	"github.com/bytedance/sonic/internal/rt"
)

// Matched diagnostic fixture shared with the retained C and Go baselines.
// Stage times are not additive; ParseBatch excludes restoration and runs on
// warmed resident storage for the default ARM64 Runs parser.
func BenchmarkARMAnalysisParseStages(b *testing.B) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(TwitterJson)); err != nil {
		b.Fatal(err)
	}
	for _, input := range []struct{ name, src string }{
		{"Twitter", TwitterJson},
		{"Compact", compact.String()},
	} {
		b.Run(input.name, func(b *testing.B) {
			b.Run("Binding", func(b *testing.B) { armAnalysisParseStages[TwitterStruct](b, input.src, false) })
			b.Run("Generic", func(b *testing.B) { armAnalysisParseStages[interface{}](b, input.src, true) })
		})
	}
}

func armAnalysisParseStages[T any](b *testing.B, src string, eface bool) {
	const options = uint64(1 << _F_copy_string)
	var want T
	if err := json.Unmarshal([]byte(src), &want); err != nil {
		b.Fatal(err)
	}
	typ := rt.UnpackType(reflect.TypeOf((*T)(nil)).Elem())
	dec, err := findOrCompile(typ)
	if err != nil {
		b.Fatal(err)
	}
	prepare := func() *Parser {
		p := newParser(src, 0, options)
		p.isEface = eface
		return p
	}
	check := func(p *Parser) {
		if p.Pos() != len(src) {
			b.Fatalf("parser position=%d, want=%d", p.Pos(), len(src))
		}
		ctx := Context{Parser: p}
		var got T
		if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil || !reflect.DeepEqual(got, want) {
			b.Fatalf("parsed tape mismatch: %v", err)
		}
	}
	warm := prepare()
	if code := warm.parse(); code != SONIC_OK {
		b.Fatal(code)
	}
	check(warm)
	warm.free()

	b.Run("FromDom", func(b *testing.B) {
		p := prepare()
		if code := p.parse(); code != SONIC_OK {
			b.Fatal(code)
		}
		check(p)
		defer p.free()
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ctx := Context{Parser: p}
			var got T
			if err := dec.FromDom(unsafe.Pointer(&got), ctx.Root(), &ctx); err != nil {
				b.Fatal(err)
			}
		}
		b.StopTimer()
		check(p)
	})

	b.Run("Prepare", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := prepare()
			p.free()
		}
	})
	b.Run("PrepareAndParse", func(b *testing.B) {
		b.SetBytes(int64(len(src)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			p := prepare()
			code, pos := p.parse(), p.Pos()
			p.free()
			if code != SONIC_OK || pos != len(src) {
				b.Fatalf("parse code=%d position=%d", code, pos)
			}
		}
		b.StopTimer()
		p := prepare()
		if code := p.parse(); code != SONIC_OK {
			b.Fatal(code)
		}
		check(p)
		p.free()
	})
	b.Run("ParseBatch", func(b *testing.B) {
		batch := 32
		if b.N < batch {
			batch = b.N
		}
		parsers := make([]*Parser, batch)
		codes := make([]ErrorCode, batch)
		for i := range parsers {
			parsers[i] = prepare()
			if code := parsers[i].parse(); code != SONIC_OK {
				b.Fatal(code)
			}
			check(parsers[i])
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
			n := batch
			if b.N-done < n {
				n = b.N - done
			}
			for _, p := range parsers[:n] {
				copy(p.padded, src)
				p.cur = p.start
				p.nbuf = nodeBuf{}
				p.nbuf.init(p.nodes)
				p._nbk = _nospaceBlock{}
			}
			b.StartTimer()
			for i, p := range parsers[:n] {
				codes[i] = p.parse()
			}
			b.StopTimer()
			for i, p := range parsers[:n] {
				if codes[i] != SONIC_OK || p.Pos() != len(src) {
					b.Fatalf("parse code=%d position=%d", codes[i], p.Pos())
				}
			}
			done += n
		}
		for _, p := range parsers {
			check(p)
		}
	})
}
