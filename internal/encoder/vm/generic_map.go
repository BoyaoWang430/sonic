package vm

import (
	"encoding/json"
	"math"
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
			*b = alg.QuoteKey(*b, *(*string)(it.It.K))
			if err := encodeAnyValue(b, *(*any)(it.It.V), s, flags); err != nil {
				return err
			}
			alg.IteratorNext(it)
		}
		return nil
	}
	first := true
	for key, value := range m {
		if !first {
			*b = append(*b, ',')
		}
		first = false
		*b = alg.QuoteKey(*b, key)
		if err := encodeAnyValue(b, value, s, flags); err != nil {
			return err
		}
	}
	return nil
}

func encodeAnyValue(b *[]byte, val any, s *vars.Stack, flags uint64) error {
	switch v := val.(type) {
	case nil:
		*b = append(*b, 'n', 'u', 'l', 'l')
		return nil
	case string:
		*b = alg.Quote(*b, v, false)
		return nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			if flags&(1<<alg.BitEncodeNullForInfOrNan) != 0 {
				*b = append(*b, 'n', 'u', 'l', 'l')
				return nil
			}
			return vars.ERR_nan_or_infinite
		}
		*b = alg.F64toa(*b, v)
		return nil
	case bool:
		if v {
			*b = append(*b, 't', 'r', 'u', 'e')
		} else {
			*b = append(*b, 'f', 'a', 'l', 's', 'e')
		}
		return nil
	case int:
		*b = alg.I64toa(*b, int64(v))
		return nil
	case int64:
		*b = alg.I64toa(*b, v)
		return nil
	case uint64:
		*b = alg.U64toa(*b, v)
		return nil
	case json.Number:
		if v == "" {
			*b = append(*b, '0')
		} else if !alg.IsValidNumber(string(v)) {
			return vars.Error_number(v)
		} else {
			*b = append(*b, v...)
		}
		return nil
	case map[string]any:
		if v == nil {
			if flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*b = append(*b, '{', '}')
			} else {
				*b = append(*b, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*b = append(*b, '{', '}')
			return nil
		}
		if !s.Save(0, 0, nil, nil) || !s.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*b = append(*b, '{')
		if err := encodeStringAnyMap(b, v, s, flags); err != nil {
			return err
		}
		s.Drop()
		s.Drop()
		*b = append(*b, '}')
		return nil
	case []any:
		if v == nil {
			if flags&(1<<alg.BitNoNullSliceOrMap) != 0 {
				*b = append(*b, '[', ']')
			} else {
				*b = append(*b, 'n', 'u', 'l', 'l')
			}
			return nil
		}
		if len(v) == 0 {
			*b = append(*b, '[', ']')
			return nil
		}
		if !s.Save(0, 0, nil, nil) {
			return vars.ERR_too_deep
		}
		*b = append(*b, '[')
		for i, elem := range v {
			if i > 0 {
				*b = append(*b, ',')
			}
			if err := encodeAnyValue(b, elem, s, flags); err != nil {
				return err
			}
		}
		s.Drop()
		*b = append(*b, ']')
		return nil
	default:
		slot := mapValuePool.Get().(*any)
		*slot = val
		e := (*rt.GoEface)(unsafe.Pointer(slot))
		err := EncodeTypedPointer(b, e.Type, &e.Value, s, flags)
		*slot = nil
		mapValuePool.Put(slot)
		return err
	}
}
