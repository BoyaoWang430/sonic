package vm

import (
	"sync"
	"unsafe"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/vars"
	"github.com/bytedance/sonic/internal/rt"
)

var stringAnyMapType = rt.MapType(rt.UnpackType(vars.StringAnyMapType))

var mapValuePool = sync.Pool{New: func() any { return new(any) }}

// The surrounding instructions handle braces, nil maps, and stack depth.
func encodeStringAnyMap(b *[]byte, m map[string]any, s *vars.Stack, flags uint64) error {
	if flags&(1<<alg.BitSortMapKeys) != 0 {
		e := rt.UnpackEface(m)
		it, err := alg.IteratorStart(stringAnyMapType, e.Value, flags)
		if err != nil {
			return err
		}
		defer alg.IteratorStop(it)
		first := true
		for it.It.K != nil {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.Quote(*b, *(*string)(it.It.K), false)
			*b = append(*b, ':')
			value := (*rt.GoEface)(it.It.V)
			if err := EncodeTypedPointer(b, value.Type, &value.Value, s, flags); err != nil {
				return err
			}
			alg.IteratorNext(it)
		}
		return nil
	}
	// Nested encoding saves this address in the VM state stack.
	slot := mapValuePool.Get().(*any)
	defer func() {
		*slot = nil
		mapValuePool.Put(slot)
	}()
	e := (*rt.GoEface)(unsafe.Pointer(slot))
	first := true
	for key, value := range m {
		*slot = value
		if !first {
			*b = append(*b, ',')
		}
		first = false
		*b = alg.Quote(*b, key, false)
		*b = append(*b, ':')
		if err := EncodeTypedPointer(b, e.Type, &e.Value, s, flags); err != nil {
			return err
		}
	}
	return nil
}
