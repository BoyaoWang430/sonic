//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"strings"
	"testing"

	"github.com/bytedance/sonic/internal/native/types"
)

func TestRISCVGetByPath(t *testing.T) {
	type namedString string
	type namedInt int
	for _, tt := range []struct {
		src  string
		path []interface{}
		want string
	}{
		{` {"asdf": [null, true, false, 1, 2.0, -3]}, 1234.5`, []interface{}{"asdf", 4}, "2.0"},
		{` {"": {"a\"b": [1, "ok"]}} `, []interface{}{"", `a"b`, 1}, `"ok"`},
		{`{"\u0061":3}`, []interface{}{"a"}, "3"},
		{`{"\ud83d\ude0a":4}`, []interface{}{"😊"}, "4"},
		{`{"x\q":0,"ok":2}`, []interface{}{"ok"}, "2"},
		{`{"\u0078\q":0,"ok":2}`, []interface{}{"ok"}, "2"},
		{`{"bad":[invalid],"ok":2}`, []interface{}{"ok"}, "2"},
		{`{"a":[7]}`, []interface{}{namedString("a"), namedInt(0)}, "7"},
		{`{"a":1,"a":2}`, []interface{}{"a"}, "1"},
		{` [1,2] `, nil, "[1,2]"},
	} {
		for _, strict := range []bool{false, true} {
			var m *types.StateMachine
			if strict {
				m = new(types.StateMachine)
			}
			p := 0
			start := GetByPath(&tt.src, &p, &tt.path, m)
			if start < 0 || p < start || p > len(tt.src) || tt.src[start:p] != tt.want {
				t.Errorf("src=%q path=%v strict=%v: start=%d end=%d want=%q", tt.src, tt.path, strict, start, p, tt.want)
			}
		}
	}
}

func TestRISCVGetByPathErrors(t *testing.T) {
	for _, tt := range []struct {
		src  string
		path []interface{}
		err  types.ParsingError
	}{
		{`{}`, []interface{}{"a"}, types.ERR_NOT_FOUND},
		{`[]`, []interface{}{0}, types.ERR_NOT_FOUND},
		{`[1]`, []interface{}{1}, types.ERR_NOT_FOUND},
		{`[1]`, []interface{}{-1}, types.ERR_UNSUPPORT_TYPE},
		{`[1]`, []interface{}{int64(0)}, types.ERR_UNSUPPORT_TYPE},
		{`{}`, []interface{}{nil}, types.ERR_UNSUPPORT_TYPE},
		{`[]`, []interface{}{"a"}, types.ERR_INVALID_CHAR},
		{`{}`, []interface{}{0}, types.ERR_INVALID_CHAR},
		{`{"a" 1}`, []interface{}{"a"}, types.ERR_INVALID_CHAR},
		{`{"a\q":1}`, []interface{}{"ab"}, types.ERR_INVALID_ESCAPE},
		{`{"\u12x4":1}`, []interface{}{"a"}, types.ERR_INVALID_CHAR},
		{`{"\ud800":1}`, []interface{}{"a"}, types.ERR_INVALID_UNICODE},
		{`{"\ud800\ud800":1}`, []interface{}{"a"}, types.ERR_INVALID_UNICODE},
		{`{"\u0061\q":1}`, []interface{}{"a"}, types.ERR_NOT_FOUND},
		{`{"abc`, []interface{}{"abc"}, types.ERR_EOF},
		{`[`, []interface{}{0}, types.ERR_INVALID_CHAR},
		{"", []interface{}{"a"}, types.ERR_INVALID_CHAR},
	} {
		p := 0
		ret := GetByPath(&tt.src, &p, &tt.path, &types.StateMachine{})
		if ret != -int(tt.err) || p < 0 || p > len(tt.src) {
			t.Errorf("src=%q path=%v: ret=%d end=%d want error=%d", tt.src, tt.path, ret, p, tt.err)
		}
	}
	src := `{"a\q":1}`
	path := []interface{}{"ab"}
	p := 0
	GetByPath(&src, &p, &path, &types.StateMachine{})
	if p != strings.Index(src, `\q`)+1 {
		t.Fatalf("invalid key escape cursor = %d", p)
	}
	src = `{"a":[invalid]}`
	path = []interface{}{"a"}
	p = 0
	if ret := GetByPath(&src, &p, &path, nil); ret < 0 || src[ret:p] != "[invalid]" {
		t.Fatalf("nil StateMachine should use fast target skip: ret=%d end=%d", ret, p)
	}
	p = 0
	if ret := GetByPath(&src, &p, &path, &types.StateMachine{}); ret >= 0 {
		t.Fatalf("StateMachine should validate target structure: ret=%d end=%d", ret, p)
	}
}
