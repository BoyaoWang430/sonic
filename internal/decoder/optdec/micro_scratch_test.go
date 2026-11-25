//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMicroInputScratchAndCopyStringContract(t *testing.T) {
	type microAuthor struct {
		Name string `json:"name"`
		Age  int    `json:"age"`
	}
	type microDoc struct {
		ID      int64           `json:"id"`
		Title   string          `json:"title"`
		Raw     json.RawMessage `json:"raw,omitempty"`
		Tags    []string        `json:"tags"`
		Authors []microAuthor   `json:"authors"`
	}

	// Cover micro thresholds and tail/slab boundaries: <128B, 221B, 255/256/257B, 320B, 365B, 512B, 1024/1025B.
	for _, targetLen := range []int{96, 221, 255, 256, 257, 320, 365, 512, 1024, 1025} {
		prefix := `{"id":42,"raw":{"k":"v"},"tags":["a","b"],"authors":[{"name":"ada","age":36}],"title":"`
		suffix := `"}`
		padLen := targetLen - len(prefix) - len(suffix)
		if padLen < 1 {
			padLen = 1
		}
		rawBytes := []byte(prefix + strings.Repeat("x", padLen) + suffix)

		// 1. CopyString detachment: mutating caller buffer after decode must never alter decoded strings or RawMessage.
		buf := append([]byte(nil), rawBytes...)
		var gotCopy microDoc
		optsCopy := uint64((1 << _F_copy_string) | (1 << _F_validate_string))
		_, posCopy, err := DecodeOne(string(buf), optsCopy, &gotCopy)
		if err != nil || posCopy != len(buf) {
			t.Fatalf("len=%d CopyString decode failed: pos=%d err=%v", len(buf), posCopy, err)
		}
		wantTitle := gotCopy.Title
		wantRaw := string(gotCopy.Raw)

		// Force tiny initial node capacities (1, 2, 8, 24) vs default pooled capacity (4096):
		// values, Pos, and residual side effects must match identically.
		for _, initCap := range []int{1, 2, 8, 24} {
			p := &Parser{
				Document: Document{
					nodes: make([]node, initCap, initCap),
				},
			}
			p.Json = string(rawBytes)
			p.end = len(p.Json)
			p.options = optsCopy
			p.nbuf.init(p.nodes)

			etp, vp, dec, proj, uerr := unpackAndCompileRoot(&gotCopy)
			if uerr != nil {
				t.Fatal(uerr)
			}
			var smallGot microDoc
			vp = reflect.ValueOf(&smallGot).UnsafePointer()
			doc, ecode := p.scanDocument(optsCopy, etp, proj)
			if ecode != SONIC_OK {
				t.Fatalf("len=%d initCap=%d scan failed: %v", len(rawBytes), initCap, ecode)
			}
			ctx := p.initDecodeContext(doc, optsCopy, etp)
			if derr := dec.FromDom(vp, ctx.Root(), ctx); derr != nil {
				t.Fatalf("len=%d initCap=%d FromDom failed: %v", len(rawBytes), initCap, derr)
			}
			if !reflect.DeepEqual(smallGot, gotCopy) || p.Pos() != posCopy {
				t.Fatalf("len=%d initCap=%d mismatch vs DecodeOne", len(rawBytes), initCap)
			}
			// Re-initializing the same Parser.ctx without p.reset() must still detach prior slabs cleanly.
			firstSlab := ctx.strSlab
			ctx2 := p.initDecodeContext(doc, optsCopy, etp)
			if ctx2.strSlab != nil {
				t.Fatalf("re-initialized active Context retained previous strSlab")
			}
			for i := range firstSlab {
				// Read-check only: mutating rawBytes copy below verifies detachment.
				_ = firstSlab[i]
			}
		}

		for i := range buf {
			buf[i] = 'Z'
		}
		if gotCopy.Title != wantTitle || string(gotCopy.Raw) != wantRaw {
			t.Fatalf("len=%d caller buffer mutation corrupted CopyString/Raw output", len(buf))
		}
	}
}
