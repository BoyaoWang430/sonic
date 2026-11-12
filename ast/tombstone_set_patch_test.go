//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

package ast_test

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
	"github.com/stretchr/testify/require"
)

func TestNodeSetCorruptJSONNoCrash(t *testing.T) {
	badObjectInputs := []string{
		`{bad}`,
		`{"a": 1`,
		`{"a": `,
		`{"a": [1, 2`,
		`{"k1": "v1", "k2": tru}`,
	}
	for _, raw := range badObjectInputs {
		// 1. _V_RAW node from Searcher
		root, err := sonic.GetFromString(raw)
		if err == nil {
			_, setErr := root.Set("x", ast.NewString("val"))
			require.Error(t, setErr, "raw=%q should fail Set", raw)
			// Calling Set or Check again must not crash due to half-mutated state.
			_, setErr2 := root.Set("y", ast.NewNumber("2"))
			require.Error(t, setErr2, "raw=%q second Set should also fail cleanly", raw)
		}

		// 2. _V_OBJECT_LAZY node from NewParserObj
		p := ast.NewParserObj(raw)
		node, perr := p.Parse()
		if perr == 0 {
			_, setErr := node.Set("x", ast.NewString("val"))
			require.Error(t, setErr, "lazy=%q should fail Set", raw)

			p2 := ast.NewParserObj(raw)
			node2, perr2 := p2.Parse()
			if perr2 == 0 {
				_, unsetErr := node2.Unset("x")
				require.Error(t, unsetErr, "lazy=%q should fail Unset", raw)
			}
		}
	}

	// _V_ARRAY_LAZY SetByIndex on malformed array must return syntax error, not ErrNotExist.
	pa := ast.NewParserObj(`[1, 2`)
	arrNode, perr := pa.Parse()
	require.Zero(t, perr)
	ok, err := arrNode.SetByIndex(5, ast.NewNumber("9"))
	require.False(t, ok)
	require.Error(t, err)
	require.NotErrorIs(t, err, ast.ErrNotExist)
}

func TestSearcherCopyDoesNotMutateOrRace(t *testing.T) {
	raw := `{"a":{"b":"hello"},"arr":[10,20,30],"c":"world"}`
	s := ast.NewSearcher(raw)
	require.False(t, s.CopyReturn)

	nCopy, err := s.GetByPathCopy("a", "b")
	require.NoError(t, err)
	strCopy, err := nCopy.String()
	require.NoError(t, err)
	require.Equal(t, "hello", strCopy)
	require.False(t, s.CopyReturn, "GetByPathCopy must not mutate Searcher.CopyReturn")

	manyCopy, err := s.GetManyByPathCopy([]interface{}{"a", "b"}, []interface{}{"c"})
	require.NoError(t, err)
	require.Len(t, manyCopy, 2)
	require.False(t, s.CopyReturn, "GetManyByPathCopy must not mutate Searcher.CopyReturn")

	// Concurrent calls on a shared *Searcher must be race-free under -race.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for iter := 0; iter < 50; iter++ {
				if (id+iter)%2 == 0 {
					n, err := s.GetByPath("arr", 1)
					require.NoError(t, err)
					v, err := n.Int64()
					require.NoError(t, err)
					require.Equal(t, int64(20), v)
				} else {
					n, err := s.GetByPathCopy("c")
					require.NoError(t, err)
					v, err := n.String()
					require.NoError(t, err)
					require.Equal(t, "world", v)
				}
				ns, err := s.GetManyByPathCopy([]interface{}{"a", "b"}, []interface{}{"arr", 2})
				require.NoError(t, err)
				require.Len(t, ns, 2)
			}
		}(g)
	}
	wg.Wait()
	require.False(t, s.CopyReturn)
}

func TestPatchByPathFloatFormattingConsistency(t *testing.T) {
	src := []byte(`{"f64":0,"f32":0}`)
	patched, err := ast.PatchByPath(src,
		ast.PathPatch{Path: []interface{}{"f64"}, Value: float64(1000000)},
		ast.PathPatch{Path: []interface{}{"f32"}, Value: float32(1000000)},
	)
	require.NoError(t, err)

	want, err := sonic.Marshal(struct {
		F64 float64 `json:"f64"`
		F32 float32 `json:"f32"`
	}{F64: 1000000, F32: 1000000})
	require.NoError(t, err)
	require.Equal(t, string(want), string(patched))
}

func TestLoadsUseNumberReturnsSyntaxError(t *testing.T) {
	_, _, errLoads := ast.Loads(`{"a":`)
	_, _, errUseNum := ast.LoadsUseNumber(`{"a":`)
	require.Error(t, errLoads)
	require.Error(t, errUseNum)
	require.Equal(t, reflect.TypeOf(errLoads), reflect.TypeOf(errUseNum))
	require.Equal(t, errLoads.Error(), errUseNum.Error())
	require.Contains(t, errUseNum.Error(), "Syntax error at index")
}

func TestMaterializedFastPathsAndTokenObjectSWAR(t *testing.T) {
	// Exercise tokenObject SWAR short/medium/long keys, backslash-after-quote in same 8B word, and escaped keys.
	raw := `[{"a":"\\","abcdefghi":"0123456789ab","seventeen_byte_key":"val_over_sixteen_bytes","esc\"key":"esc\"val","sp" : "x"},{"target":99}]`
	target, err := sonic.Get([]byte(raw), 1, "target")
	require.NoError(t, err)
	v, err := target.Int64()
	require.NoError(t, err)
	require.Equal(t, int64(99), v)

	r0 := ast.NewRaw(raw)
	require.NoError(t, r0.Check())

	// Materialized V_ARRAY (>16 elements to cover both head and tail chunks) + UnsetByIndex + Index bounds.
	var arrNodes []ast.Node
	for i := 0; i < 20; i++ {
		arrNodes = append(arrNodes, ast.NewNumber(fmt.Sprintf("%d", i)))
	}
	arr := ast.NewArray(arrNodes)
	n18, err := arr.Index(18).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(18), n18)
	require.Nil(t, arr.Index(20))
	require.Nil(t, arr.Index(-1))

	ok, err := arr.UnsetByIndex(2)
	require.NoError(t, err)
	require.True(t, ok)
	shifted, err := arr.Index(2).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(3), shifted)
	ok, err = arr.UnsetByIndex(99)
	require.Error(t, err)
	require.False(t, ok)

	// Materialized V_OBJECT (>16 pairs) + Get + Index + UnsetByIndex.
	var pairs []ast.Pair
	for i := 0; i < 20; i++ {
		pairs = append(pairs, ast.NewPair(fmt.Sprintf("k%d", i), ast.NewNumber(fmt.Sprintf("%d", i*10))))
	}
	obj := ast.NewObject(pairs)
	gv, err := obj.Get("k17").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(170), gv)
	require.Nil(t, obj.Get("missing"))
	iv, err := obj.Index(17).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(170), iv)
	require.False(t, obj.Index(25).Valid())
	ok, err = obj.UnsetByIndex(1)
	require.NoError(t, err)
	require.True(t, ok)
	afterUnset, err := obj.Index(1).Int64()
	require.NoError(t, err)
	require.Equal(t, int64(20), afterUnset)
}

func TestObjectTombstoneEmptyKeyAndIndex(t *testing.T) {
	// 1. Object containing a valid empty key "": Unset("a") must not shadow "": 42 or produce duplicate "".
	n1, err := sonic.GetFromString(`{"a": 1, "": 42}`)
	require.NoError(t, err)
	existed, err := n1.Unset("a")
	require.NoError(t, err)
	require.True(t, existed)

	emptyVal, err := n1.Get("").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(42), emptyVal)

	replaced, err := n1.Set("", ast.NewNumber("99"))
	require.NoError(t, err)
	require.True(t, replaced)

	out1, err := n1.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, `{"":99}`, string(out1))

	m1, err := n1.MapUseNode()
	require.NoError(t, err)
	require.Len(t, m1, 1)
	mn := m1[""]
	v99, err := mn.Int64()
	require.NoError(t, err)
	require.Equal(t, int64(99), v99)

	// 2. Object without empty key: Unset("a") must still return nil / !Valid() / ErrNotExist on Get("").
	n2, err := sonic.GetFromString(`{"a": 1, "b": 2}`)
	require.NoError(t, err)
	existed, err = n2.Unset("a")
	require.NoError(t, err)
	require.True(t, existed)
	gEmpty := n2.Get("")
	require.Nil(t, gEmpty)
	require.False(t, gEmpty.Valid())
	require.ErrorIs(t, gEmpty.Check(), ast.ErrNotExist)

	// 3. Large indexed object (>16 keys, triggers BuildIndex) with "", Unset, UnsetByIndex, SortKeys, Pop.
	var pairs []ast.Pair
	pairs = append(pairs, ast.NewPair("", ast.NewNumber("777")))
	for i := 0; i < 20; i++ {
		pairs = append(pairs, ast.NewPair(fmt.Sprintf("k%02d", i), ast.NewNumber(fmt.Sprintf("%d", i))))
	}
	bigObj := ast.NewObject(pairs)
	require.NoError(t, bigObj.LoadAll())

	ok, err := bigObj.Unset("k00")
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, bigObj.Get("k00"))

	ok, err = bigObj.UnsetByIndex(1) // removes k01
	require.NoError(t, err)
	require.True(t, ok)
	require.Nil(t, bigObj.Get("k01"))

	// Unset trailing k19 then Pop (exercises Pop over trailing tombstones without deleting "" from index).
	ok, err = bigObj.Unset("k19")
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, bigObj.Pop()) // pops k18
	require.Nil(t, bigObj.Get("k18"))

	require.NoError(t, bigObj.SortKeys(false))
	v777, err := bigObj.Get("").Int64()
	require.NoError(t, err)
	require.Equal(t, int64(777), v777)

	// 4. Explicit Set("", Node{}) placeholder remains addressable via Get("") (TestNodeAny contract).
	placeholderObj := ast.Node{}
	_, err = placeholderObj.Set("", ast.Node{})
	require.NoError(t, err)
	require.NoError(t, placeholderObj.Get("").AddAny(nil))
	pOut, err := placeholderObj.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, `{"":[null]}`, string(pOut))

	// 5. Soft-deleted array ArrayUseNode consistency.
	arrNode, err := sonic.GetFromString(`[10, 20, 30]`)
	require.NoError(t, err)
	ok, err = arrNode.UnsetByIndex(0)
	require.NoError(t, err)
	require.True(t, ok)
	elems, err := arrNode.ArrayUseNode()
	require.NoError(t, err)
	require.Len(t, elems, 2)
	e0, _ := elems[0].Int64()
	e1, _ := elems[1].Int64()
	require.Equal(t, int64(20), e0)
	require.Equal(t, int64(30), e1)
}

func TestMultiChunkEscapedStringGet(t *testing.T) {
	for _, chunkPad := range []int{60, 62, 63, 64, 125, 127, 128, 253, 255, 256, 320} {
		orig := strings.Repeat("x", chunkPad) + "\n\t\"\\\u4e2d\U0001f680" + strings.Repeat("y", 150) + "\r\nend"
		encoded, err := sonic.Marshal(map[string]string{"k": orig})
		require.NoError(t, err)

		node, err := sonic.Get(encoded, "k")
		require.NoError(t, err)
		gotStr, err := node.String()
		require.NoError(t, err)
		require.Equal(t, orig, gotStr)
	}
}
