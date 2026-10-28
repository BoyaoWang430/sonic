//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package alg

import (
	"fmt"
	"reflect"
	"runtime"
	"strconv"
	"testing"

	"github.com/bytedance/sonic/internal/rt"
)

type iteratorLargeValue struct {
	Padding [160]byte
	Pointer *int
	Bytes   []byte
}

func iteratorPayload(i int) iteratorLargeValue {
	p := new(int)
	*p = i
	v := iteratorLargeValue{Pointer: p, Bytes: []byte(strconv.Itoa(i))}
	v.Padding[0], v.Padding[159] = byte(i), byte(i+1)
	return v
}

// The iterator must retain entries after the caller releases the map.
func detachedMapIterator(t *testing.T, makeMap func() any, flags uint64) (*MapIterator, reflect.Type) {
	t.Helper()
	value := makeMap()
	eface := rt.UnpackEface(value)
	it, err := IteratorStart(rt.MapType(eface.Type), eface.Value, flags)
	if err != nil {
		t.Fatal(err)
	}
	return it, reflect.TypeOf(value).Elem()
}

func TestMapIteratorGCOwnership(t *testing.T) {
	for _, flags := range []uint64{0, 1 << BitSortMapKeys} {
		for _, kind := range []string{"indirect", "interface", "pointer"} {
			t.Run(fmt.Sprintf("%s/sort=%v", kind, flags != 0), func(t *testing.T) {
				it, elem := detachedMapIterator(t, func() any {
					switch kind {
					case "indirect":
						m := make(map[string]iteratorLargeValue)
						for i := 0; i < 16; i++ {
							m[strconv.Itoa(i)] = iteratorPayload(i)
						}
						return m
					case "interface":
						m := make(map[string]any)
						for i := 0; i < 16; i++ {
							v := iteratorPayload(i)
							m[strconv.Itoa(i)] = &v
						}
						return m
					default:
						m := make(map[string]*iteratorLargeValue)
						for i := 0; i < 16; i++ {
							v := iteratorPayload(i)
							m[strconv.Itoa(i)] = &v
						}
						m["nil"] = nil
						return m
					}
				}, flags)
				defer IteratorStop(it)
				seen := make(map[string]bool)
				previous := ""
				for it.It.K != nil {
					key := *(*string)(it.It.K)
					valuePointer := it.It.V
					// The VM advances before encoding this value.
					IteratorNext(it)
					runtime.GC()
					got := reflect.NewAt(elem, valuePointer).Elem().Interface()
					if seen[key] || flags != 0 && key < previous {
						t.Fatalf("unexpected key %q after %q", key, previous)
					}
					seen[key], previous = true, key
					if key == "nil" {
						if got.(*iteratorLargeValue) != nil {
							t.Fatal("nil map value changed")
						}
						continue
					}
					i, _ := strconv.Atoi(key)
					var v iteratorLargeValue
					if kind == "indirect" {
						v = got.(iteratorLargeValue)
					} else {
						v = *got.(*iteratorLargeValue)
					}
					if v.Pointer == nil || *v.Pointer != i || string(v.Bytes) != key || v.Padding[0] != byte(i) || v.Padding[159] != byte(i+1) {
						t.Fatalf("value ownership lost for key %q", key)
					}
				}
				n := 16
				if kind == "pointer" {
					n++
				}
				if len(seen) != n {
					t.Fatalf("visited %d entries, want %d", len(seen), n)
				}
			})
		}
	}
}

func TestMapIteratorEmpty(t *testing.T) {
	for _, flags := range []uint64{0, 1 << BitSortMapKeys} {
		for _, value := range []map[string]int{nil, {}} {
			eface := rt.UnpackEface(value)
			it, err := IteratorStart(rt.MapType(eface.Type), eface.Value, flags)
			if err != nil || it.It.K != nil || it.It.V != nil {
				t.Fatalf("empty map: iterator=%+v err=%v", it, err)
			}
			IteratorStop(it)
		}
	}
}

func TestMapIteratorDeletion(t *testing.T) {
	value := make(map[string]int)
	for i := 0; i < 32; i++ {
		value[strconv.Itoa(i)] = i
	}
	eface := rt.UnpackEface(value)
	it, err := IteratorStart(rt.MapType(eface.Type), eface.Value, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer IteratorStop(it)
	key, ptr := *(*string)(it.It.K), it.It.V
	want := value[key]
	for k := range value {
		if k != key {
			delete(value, k)
		}
	}
	IteratorNext(it)
	runtime.GC()
	if it.It.K != nil || *(*int)(ptr) != want {
		t.Fatal("deletion changed the current value or exposed a deleted entry")
	}
}

func TestMapIteratorGrowth(t *testing.T) {
	for _, flags := range []uint64{0, 1 << BitSortMapKeys} {
		t.Run(fmt.Sprintf("sort=%v", flags != 0), func(t *testing.T) {
			value := make(map[string]*iteratorLargeValue)
			for i := 0; i < 4; i++ {
				v := iteratorPayload(i)
				value[strconv.Itoa(i)] = &v
			}
			eface := rt.UnpackEface(value)
			it, err := IteratorStart(rt.MapType(eface.Type), eface.Value, flags)
			if err != nil {
				t.Fatal(err)
			}
			defer IteratorStop(it)
			key, ptr := *(*string)(it.It.K), it.It.V
			want := value[key]
			// Retain the current value through map growth and GC.
			for i := 4; i < 4096; i++ {
				v := iteratorPayload(i)
				value[strconv.Itoa(i)] = &v
			}
			value = nil
			eface = rt.GoEface{}
			IteratorNext(it)
			runtime.GC()
			if got := *(**iteratorLargeValue)(ptr); got != want || string(got.Bytes) != key {
				t.Fatalf("growth changed the saved value for key %q", key)
			}
			seen := map[string]bool{key: true}
			for it.It.K != nil {
				k, p := *(*string)(it.It.K), it.It.V
				IteratorNext(it)
				if seen[k] {
					t.Fatalf("growth repeated key %q", k)
				}
				seen[k] = true
				v := *(**iteratorLargeValue)(p)
				if string(v.Bytes) != k {
					t.Fatalf("growth mismatched value for key %q", k)
				}
			}
			for i := 0; i < 4; i++ {
				if !seen[strconv.Itoa(i)] {
					t.Fatalf("growth lost original key %d", i)
				}
			}
		})
	}
}
