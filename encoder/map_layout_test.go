package encoder_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bytedance/sonic/encoder"
)

var encodedMap []byte

type mapPayload struct {
	N       int
	Text    string
	Padding [160]byte
	Pointer *int
}

func mapFixtures() []struct {
	name  string
	value any
} {
	generic := make(map[string]any, 1024)
	stringsMap := make(map[string]string, 1024)
	integers := make(map[int64]mapPayload, 1024)
	for i := 0; i < 1024; i++ {
		key := fmt.Sprintf("key/%05d", (i*313)%1024)
		switch i % 4 {
		case 0:
			generic[key] = i
		case 1:
			generic[key] = strings.Repeat("sample", 12)
		case 2:
			generic[key] = i%3 == 0
		case 3:
			generic[key] = nil
		}
		stringsMap[key] = strings.Repeat("quoted<&>value ", 6)
		n := new(int)
		*n = i
		integers[int64(i)*1000000007-1000000000000] = mapPayload{N: i, Text: key, Pointer: n}
	}
	return []struct {
		name  string
		value any
	}{
		{"Generic1024", generic}, {"Strings1024", stringsMap}, {"IndirectIntKeys1024", integers},
	}
}

func checkMapFixture(t testing.TB, v any, sorted bool) {
	t.Helper()
	options := encoder.EscapeHTML | encoder.CompactMarshaler
	if sorted {
		options |= encoder.SortMapKeys
	}
	got, err := encoder.Encode(v, options)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if sorted {
		if !bytes.Equal(got, want) {
			t.Fatal("sorted bytes differ")
		}
		return
	}
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("unordered map values differ")
	}
}

func TestMapLayout(t *testing.T) {
	for _, f := range mapFixtures() {
		for _, sorted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sorted=%v", f.name, sorted), func(t *testing.T) { checkMapFixture(t, f.value, sorted) })
		}
	}
}

func BenchmarkMapLayout(b *testing.B) {
	for _, f := range mapFixtures() {
		for _, sorted := range []bool{false, true} {
			name := "Unsorted"
			if sorted {
				name = "Sorted"
			}
			b.Run(f.name+"/"+name, func(b *testing.B) {
				checkMapFixture(b, f.value, sorted)
				options := encoder.EscapeHTML | encoder.CompactMarshaler
				if sorted {
					options |= encoder.SortMapKeys
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					var err error
					encodedMap, err = encoder.Encode(f.value, options)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
