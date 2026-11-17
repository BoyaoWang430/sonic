package alg

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestQuoteSafeHTMLBits64AllBytes(t *testing.T) {
	for lane := 0; lane < 8; lane++ {
		for b := 0; b < 256; b++ {
			w := uint64(0x4141414141414141)
			w &^= uint64(0xff) << (lane * 8)
			w |= uint64(b) << (lane * 8)
			gotUnsafe := (quoteSafeHTMLBits64(w) & saniMSB64) != 0
			wantSafe := isSafeHTMLASCIIByte(byte(b))
			if gotUnsafe == wantSafe {
				t.Fatalf("lane=%d byte=0x%02x: gotUnsafe=%v wantSafe=%v", lane, b, gotUnsafe, wantSafe)
			}

			// Also verify non-UTF8-validating EscapeHTML SWAR classifier on UTF-8 background bytes (0xe6 = Chinese '未').
			wUTF8 := uint64(0xe69caae69caae69c)
			wUTF8 &^= uint64(0xff) << (lane * 8)
			wUTF8 |= uint64(b) << (lane * 8)
			gotNoValUnsafe := (quoteSafeHTMLNoValBits64(wUTF8) & saniMSB64) != 0
			wantNoValSafe := isSafeHTMLNoValByte(byte(b))
			if gotNoValUnsafe == wantNoValSafe {
				t.Fatalf("noVal lane=%d byte=0x%02x: gotUnsafe=%v wantSafe=%v", lane, b, gotNoValUnsafe, wantNoValSafe)
			}
		}
	}
}

func TestQuoteFlagsHTMLAndUTF8(t *testing.T) {
	inputs := []string{
		"",
		"a",
		"ab",
		"abc",
		"abcd1234",
		"https://si0.twimg.com/profile_images/2359746665/1v6zfgqo8g0d3mk7ii5s_normal.jpeg",
		"<a href=\"http://twitter.com/download/iphone\" rel=\"nofollow\">Twitter for iPhone</a>",
		"a&b<c>d\u2028e\u2029f",
		"\u2028\u2028\xe2",
		"bad\xffutf8\xe2\x80<tag>",
		"\xe2\x80\xe2\x80\xa8",
		"先ほどRTしたツイート、日本語テキストのみ",
		"未来简史-从智人到智神-先ほどRTしたツイート、日本語テキストのみ\u2028tail",
	}
	for _, s := range inputs {
		for _, double := range []bool{false, true} {
			for _, flags := range []uint64{
				1 << BitEscapeHTML,
				1 << BitValidateString,
				(1 << BitEscapeHTML) | (1 << BitValidateString),
			} {
				raw := Quote(nil, s, double)
				want := SanitizeAppended(append([]byte(nil), raw...), 0, flags)
				got := QuoteFlags(nil, s, double, flags)
				if !bytes.Equal(got, want) {
					t.Fatalf("s=%q double=%v flags=%d:\ngot:  %q\nwant: %q", s, double, flags, got, want)
				}
				if !double {
					key := ",\"field\":"
					keyBlob := append([]byte(key), make([]byte, 32)...)
					rawKey := QuoteWithPaddedKey(nil, &keyBlob[0], len(key), s)
					wantKey := SanitizeAppended(append([]byte(nil), rawKey...), len(key), flags)
					gotKey := QuoteWithPaddedKeyFlags(make([]byte, 0, 256), &keyBlob[0], len(key), s, flags)
					if !bytes.Equal(gotKey, wantKey) {
						t.Fatalf("padded s=%q flags=%d:\ngot:  %q\nwant: %q", s, flags, gotKey, wantKey)
					}
					rawQKey := QuoteKey(nil, s)
					wantQKey := SanitizeAppended(append([]byte(nil), rawQKey...), 0, flags)
					gotQKey := QuoteKeyFlags(nil, s, flags)
					if !bytes.Equal(gotQKey, wantQKey) {
						t.Fatalf("QuoteKeyFlags s=%q flags=%d:\ngot:  %q\nwant: %q", s, flags, gotQKey, wantQKey)
					}
				}
			}
		}
		// Compare valid UTF-8 EscapeHTML against encoding/json
		if s != "\u2028\u2028\xe2" && s != "bad\xffutf8\xe2\x80<tag>" && s != "\xe2\x80\xe2\x80\xa8" {
			stdBytes, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			gotEsc := QuoteFlags(nil, s, false, 1<<BitEscapeHTML)
			if !bytes.Equal(gotEsc, stdBytes) {
				t.Fatalf("stdlib mismatch for %q: got %s want %s", s, gotEsc, stdBytes)
			}
		}
	}
}

func TestAppendCompactedJSONPreservesSpellingAndOverlapSafety(t *testing.T) {
	// Raw numeric spellings and escape sequences must never be normalized when compacting.
	raw := []byte("  { \"num0\" : -0.000 , \"exp\" : 1e+00 , \"big\" : 1E999999 , \"esc\" : \"\\/\\u0041\\u004a\" , \"arr\" : [   ] } \n\t ")
	want := `{"num0":-0.000,"exp":1e+00,"big":1E999999,"esc":"\/\u0041\u004a","arr":[]}`
	got, err := AppendCompactedJSON([]byte("prefix:"), raw, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "prefix:"+want {
		t.Fatalf("got %q, want %q", string(got), "prefix:"+want)
	}

	// Exact spare suffix finalizes in place without reallocating.
	backing := make([]byte, 0, 128)
	backing = append(backing, "hdr:"...)
	clean := `{"a":1,"b":[true,false,null]}`
	spare := backing[len(backing) : len(backing)+len(clean)]
	copy(spare, clean)
	inPlace, err := AppendCompactedJSON(backing, spare, 1<<BitEscapeHTML)
	if err != nil {
		t.Fatalf("exact spare suffix err: %v", err)
	}
	if &inPlace[0] != &backing[0] || string(inPlace) != "hdr:"+clean {
		t.Fatalf("exact spare suffix unexpected result: %q", string(inPlace))
	}

	// Overlapping non-exact spare region detaches safely before rewriting.
	ovBuf := make([]byte, 0, 128)
	ovBuf = append(ovBuf, "p:"...)
	dirty := `  { "tag" : "<a>&<b>" }  `
	ovSlice := ovBuf[len(ovBuf)+3 : len(ovBuf)+3+len(dirty)]
	copy(ovSlice, dirty)
	ovOut, err := AppendCompactedJSON(ovBuf, ovSlice, 1<<BitEscapeHTML)
	if err != nil {
		t.Fatalf("overlapping spare err: %v", err)
	}
	if string(ovOut) != `p:{"tag":"\u003ca\u003e\u0026\u003cb\u003e"}` {
		t.Fatalf("overlapping spare unexpected output: %q", string(ovOut))
	}

	// Invalid inputs preserve dst prefix and return *json.SyntaxError consistently
	// regardless of sanitize flags or preceding HTML/UTF-8 bytes.
	invalids := []string{
		"",
		" ",
		"01",
		"[01]",
		`{"a":"<b>\q"}`,
		`{"a":"\xff\q"}`,
		`{"a":1} trailing`,
		`[1,]`,
		`{,}`,
		"\"unclosed",
		"\"\x01\"",
	}
	for _, bad := range invalids {
		for _, flags := range []uint64{0, 1 << BitEscapeHTML, 1 << BitValidateString, SanitizeMask} {
			dst := []byte("keep:")
			out, cerr := AppendCompactedJSON(dst, []byte(bad), flags)
			if cerr == nil {
				t.Fatalf("expected error for %q (flags=%d), got %q", bad, flags, string(out))
			}
			if _, ok := cerr.(*json.SyntaxError); !ok {
				t.Fatalf("expected *json.SyntaxError for %q, got %T (%v)", bad, cerr, cerr)
			}
			if string(out) != "keep:" {
				t.Fatalf("dst prefix mutated on error for %q: %q", bad, string(out))
			}
		}
	}
}
