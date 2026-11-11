package vm

import (
	"encoding/json"
	"math"

	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/encoder/vars"
)

// The surrounding instructions handle braces, nil maps, and stack depth.
func encodeStringAnyMap(b *[]byte, m map[string]any, s *vars.Stack, flags uint64) error {
	sanitize := flags & alg.SanitizeMask
	if flags&(1<<alg.BitSortMapKeys) != 0 {
		n := len(m)
		if n == 0 {
			return nil
		}
		if n == 1 {
			first := true
			for key, value := range m {
				if !first {
					*b = append(*b, ',')
				}
				first = false
				*b = alg.QuoteKeyFlags(*b, key, sanitize)
				if err := encodeAnyValue(b, value, s, flags); err != nil {
					return err
				}
			}
			return nil
		}

		baseKV, baseOrd := s.ReserveMapKV(n)
		kvSlice := s.KVBuf[baseKV : baseKV+n]
		ordSlice := s.KVOrder[baseOrd : baseOrd+n]
		count := 0
		for k, v := range m {
			if count < len(kvSlice) {
				kvSlice[count] = vars.MapKVEntry{K: k, V: v}
				ordSlice[count] = count
			} else {
				s.GrowMapKVOne(k, v, count)
				kvSlice = s.KVBuf[baseKV:]
				ordSlice = s.KVOrder[baseOrd:]
			}
			count++
		}
		if count == 0 {
			s.ReleaseMapKV(baseKV, n, baseOrd)
			return nil
		}
		if count < n {
			kvSlice, ordSlice = s.TrimMapKV(baseKV, baseOrd, count)
		}
		alg.SortMapKVOrder(ordSlice, kvSlice)
		for idx := 0; idx < count; idx++ {
			if idx > 0 {
				*b = append(*b, ',')
			}
			entry := s.KVBuf[baseKV+s.KVOrder[baseOrd+idx]]
			*b = alg.QuoteKeyFlags(*b, entry.K, sanitize)
			if err := encodeAnyValue(b, entry.V, s, flags); err != nil {
				s.ReleaseMapKV(baseKV, count, baseOrd)
				return err
			}
		}
		s.ReleaseMapKV(baseKV, count, baseOrd)
		return nil
	}
	first := true
	for key, value := range m {
		if !first {
			*b = append(*b, ',')
		}
		first = false
		*b = alg.QuoteKeyFlags(*b, key, sanitize)
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
		*b = alg.QuoteFlags(*b, v, false, flags&alg.SanitizeMask)
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
		return EncodeValue(b, val, s, flags&^uint64(1<<alg.BitPointerValue))
	}
}
