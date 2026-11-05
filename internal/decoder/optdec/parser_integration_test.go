//go:build (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Token positions refer to the copied suffix, including strings and raw
// numbers whose binders read the immutable source instead of the decoded tape.
func TestARMParserDecodeOffsets(t *testing.T) {
	for _, prefix := range []string{"", "prefix ", "\xff prefix ", strings.Repeat("prefix ", 16)} {
		for _, body := range []string{
			`{"plain":"value","escaped":"a\nb","n":1.25}`,
			`["text","a\tb",12345678901234567890,1.25]`,
			`"\ud834\udd1e"`,
		} {
			for _, flags := range []uint64{0, uint64(OptionCopyString), uint64(OptionValidateString), uint64(OptionUseNumber)} {
				input := prefix + body + ` {"next":true}`
				original := input
				pos := len(prefix)
				var got, want interface{}
				if flags&uint64(OptionUseNumber) != 0 {
					decoder := json.NewDecoder(strings.NewReader(body))
					decoder.UseNumber()
					if err := decoder.Decode(&want); err != nil {
						t.Fatal(err)
					}
				} else if err := json.Unmarshal([]byte(body), &want); err != nil {
					t.Fatal(err)
				}
				if err := Decode(&input, &pos, flags, &got); err != nil {
					t.Fatalf("prefix=%q flags=%d input=%q: %v", prefix, flags, body, err)
				}
				if !reflect.DeepEqual(got, want) || pos != len(prefix)+len(body) || input != original {
					t.Fatalf("prefix=%q flags=%d: value=%#v pos=%d source=%q; want %#v pos=%d source=%q", prefix, flags, got, pos, input, want, len(prefix)+len(body), original)
				}
			}
		}
	}
}

func TestARMParserOffsetInputBoundary(t *testing.T) {
	// A consumed prefix must not extend the logical suffix into scanner padding.
	for _, prefix := range []string{"prefix ", strings.Repeat("prefix ", 16)} {
		for _, suffix := range []string{`"unterminated`, `{"plain":"unterminated`} {
			input := prefix + suffix
			pos := len(prefix)
			var got interface{}
			if err := Decode(&input, &pos, 0, &got); err == nil {
				t.Fatalf("accepted incomplete suffix %q after %d prefix bytes: %#v", suffix, len(prefix), got)
			}
		}
	}
}

func TestARMParserCorrectedSourceOwnership(t *testing.T) {
	for _, prefix := range []string{"", "prefix ", "\xff prefix "} {
		body := `{"key\u0031":"x` + "\xff" + `\ny","plain":"text"}`
		correctedBody := `{"key\u0031":"x` + "\ufffd" + `\ny","plain":"text"}`
		input := prefix + body + ` {"next":"a\tb"}`
		pos := len(prefix)
		var first map[string]string
		if err := Decode(&input, &pos, uint64(OptionValidateString), &first); err != nil {
			t.Fatal(err)
		}
		wantSource := prefix + correctedBody + ` {"next":"a\tb"}`
		if first["key1"] != "x\ufffd\ny" || first["plain"] != "text" || input != wantSource || pos != len(prefix)+len(correctedBody) {
			t.Fatalf("prefix=%q: decoded=%#v source=%q pos=%d; want source=%q pos=%d", prefix, first, input, pos, wantSource, len(prefix)+len(correctedBody))
		}
		var second map[string]string
		if err := Decode(&input, &pos, uint64(OptionValidateString), &second); err != nil || second["next"] != "a\tb" || pos != len(input) || input != wantSource {
			t.Fatalf("prefix=%q: second=%#v source=%q pos=%d err=%v", prefix, second, input, pos, err)
		}
	}
}
