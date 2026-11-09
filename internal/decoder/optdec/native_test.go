package optdec

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/davecgh/go-spew/spew"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNative(t *testing.T) {
	data := `{"a":1, "b": true,    "":    false, "d":                             null,"\"f":[], "\\": "\\", "hi": {}, "":[[[]], "", [{}]]}`
	p := newParser(data, 0, 0)
	ecode := p.parse()
	defer p.free()
	assert.Equal(t, int(ecode), 0)
	assert.Equal(t, p.Pos(), len(data))
	spew.Dump(p.nbuf.stat)
}

func TestParseNativeRetryLargeJson(t *testing.T) {
	t.Run("Object", func(t *testing.T) {
		data := "{" + strings.Repeat("\"a\":1,", 1<<20) + "\"a\":1}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 1)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
	})

	t.Run("ObjectNull", func(t *testing.T) {
		data := "{" + strings.Repeat("\"a\":null,", 1<<20) + "\"a\":null}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 1)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
	})

	t.Run("Object2", func(t *testing.T) {
		data := "{\"top\": {" + strings.Repeat("\"a\":1,", 1<<20) + "\"a\":1}, \"final\": true}"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, int(p.Pos()), len(data))
		assert.Equal(t, int(p.nbuf.stat.object), 2)
		assert.Equal(t, int(p.nbuf.stat.object_keys), 1<<20+3)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 2)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
	})

	t.Run("Array", func(t *testing.T) {
		data := "[" + strings.Repeat("1,", 1<<20) + "1]"
		p := newParser(data, 0, 0)
		ecode := p.parse()
		defer p.free()
		assert.Equal(t, int(ecode), 0)
		assert.Equal(t, p.Pos(), len(data))
		assert.Equal(t, int(p.nbuf.stat.array), 1)
		assert.Equal(t, int(p.nbuf.stat.array_elems), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.number), 1<<20+1)
		assert.Equal(t, int(p.nbuf.stat.max_depth), 1)
	})
}


func TestSubPlanSharingAndRecursiveBind(t *testing.T) {
	type sharedLeaf struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Tag  string `json:"tag"`
	}
	type parentDTO struct {
		A     sharedLeaf   `json:"a"`
		B     sharedLeaf   `json:"b"`
		C     *sharedLeaf  `json:"c"`
		Slice []sharedLeaf `json:"slice"`
	}

	dec, err := newCompiler().compileType(reflect.TypeOf(parentDTO{}))
	require.NoError(t, err)
	sd, ok := dec.(*structDecoder)
	require.True(t, ok)
	require.Len(t, sd.fields, 4)

	decA := sd.fieldDecs[0].(*structDecoder)
	decB := sd.fieldDecs[1].(*structDecoder)
	decC := sd.fieldDecs[2].(*ptrDecoder).deref.(*structDecoder)
	decSlice := sd.fieldDecs[3].(*sliceDecoder).structDec
	require.Same(t, decA, decB)
	require.Same(t, decA, decC)
	require.Same(t, decA, decSlice)
	require.Same(t, decA.fieldMap, decB.fieldMap)

	type treeNode struct {
		Val   int       `json:"val"`
		Left  *treeNode `json:"left"`
		Right *treeNode `json:"right"`
	}
	tdec, err := newCompiler().compileType(reflect.TypeOf(treeNode{}))
	require.NoError(t, err)
	tsd := tdec.(*structDecoder)
	leftRec := tsd.fieldDecs[1].(*ptrDecoder).deref.(*recuriveDecoder)
	rightRec := tsd.fieldDecs[2].(*ptrDecoder).deref.(*recuriveDecoder)
	require.NotNil(t, atomic.LoadPointer(&leftRec.dec))
	require.NotNil(t, atomic.LoadPointer(&rightRec.dec))
}

func TestContainerAsRawVariants(t *testing.T) {
	type rawEnvelope struct {
		EmptyObj  json.RawMessage `json:"empty_obj"`
		EmptyArr  json.RawMessage `json:"empty_arr"`
		FlatObj   json.RawMessage `json:"flat_obj"`
		FlatArr   json.RawMessage `json:"flat_arr"`
		NestedObj json.RawMessage `json:"nested_obj"`
		EscEnd    json.RawMessage `json:"esc_end"`
	}
	input := `{"empty_obj": {  }, "empty_arr": [ ], "flat_obj": {"x": 1, "y": "ok"}, "flat_arr": [1, true, false, null, "hi"], "nested_obj": {"a": {"b": [1, {"c": { }}]}}, "esc_end": {"k": "a\"b\nc"  }}`
	var got, want rawEnvelope
	pos := 0
	require.NoError(t, Decode(&input, &pos, 0, &got))
	require.NoError(t, json.Unmarshal([]byte(input), &want))
	require.Equal(t, want, got)
}

func BenchmarkCompileSharedSubStructs(b *testing.B) {
	type sharedSub struct {
		F1 string `json:"f1"`
		F2 int64  `json:"f2"`
		F3 bool   `json:"f3"`
		F4 string `json:"f4"`
		F5 int    `json:"f5"`
		F6 string `json:"f6"`
	}
	type bigHolder struct {
		S1  sharedSub   `json:"s1"`
		S2  sharedSub   `json:"s2"`
		S3  sharedSub   `json:"s3"`
		S4  sharedSub   `json:"s4"`
		S5  *sharedSub  `json:"s5"`
		S6  *sharedSub  `json:"s6"`
		S7  []sharedSub `json:"s7"`
		S8  []sharedSub `json:"s8"`
		S9  sharedSub   `json:"s9"`
		S10 sharedSub   `json:"s10"`
	}
	vt := reflect.TypeOf(bigHolder{})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := newCompiler().compileType(vt)
		if err != nil {
			b.Fatal(err)
		}
	}
}
