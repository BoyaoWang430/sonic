package vm

import (
	"encoding/json"
	"math"
	"reflect"
	"unsafe"

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
	if sanitize == 0 {
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
		if sanitize := flags & alg.SanitizeMask; sanitize == 0 {
			*b = alg.Quote(*b, v, false)
		} else {
			*b = alg.QuoteFlags(*b, v, false, sanitize)
		}
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
		if s == nil {
			stk := vars.NewStack()
			stk.EnterDepth(2)
			*b = append(*b, '{')
			err := encodeStringAnyMap(b, v, stk, flags)
			if err != nil {
				vars.ResetStack(stk)
				vars.FreeStack(stk)
				return err
			}
			vars.FreeStack(stk)
			*b = append(*b, '}')
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
		if s == nil {
			stk := vars.NewStack()
			stk.EnterDepth(2)
			*b = append(*b, '[')
			for i, elem := range v {
				if i > 0 {
					*b = append(*b, ',')
				}
				if err := encodeAnyValue(b, elem, stk, flags); err != nil {
					vars.ResetStack(stk)
					vars.FreeStack(stk)
					return err
				}
			}
			vars.FreeStack(stk)
			*b = append(*b, ']')
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
		if s == nil {
			stk := vars.NewStack()
			stk.EnterDepth(2)
			err := EncodeValue(b, val, stk, flags&^uint64(1<<alg.BitPointerValue))
			if err != nil {
				vars.ResetStack(stk)
			}
			vars.FreeStack(stk)
			return err
		}
		return EncodeValue(b, val, s, flags&^uint64(1<<alg.BitPointerValue))
	}
}

func sortStrStringMap(m map[string]string, s *vars.Stack) (baseKV int, baseOrd int, baseStr int, count int) {
	n := len(m)
	baseKV, baseOrd, baseStr = s.ReserveMapKVStr(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	strSlice := s.KVStr[baseStr : baseStr+n]
	for k, v := range m {
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			strSlice[count] = v
		} else {
			s.GrowMapKVStrOne(k, v, count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			strSlice = s.KVStr[baseStr:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVStr(baseKV, n, baseOrd, baseStr)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVStr(baseKV, baseOrd, baseStr, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseStr, count
}

func sortStrIntMap(m map[string]int, s *vars.Stack) (baseKV int, baseOrd int, baseU64 int, count int) {
	n := len(m)
	baseKV, baseOrd, baseU64 = s.ReserveMapKVScalar(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	u64Slice := s.KVScalar[baseU64 : baseU64+n]
	for k, v := range m {
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			u64Slice[count] = uint64(int64(v))
		} else {
			s.GrowMapKVScalarOne(k, uint64(int64(v)), count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			u64Slice = s.KVScalar[baseU64:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVScalar(baseKV, n, baseOrd, baseU64)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVScalar(baseKV, baseOrd, baseU64, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseU64, count
}

func sortStrI64Map(m map[string]int64, s *vars.Stack) (baseKV int, baseOrd int, baseU64 int, count int) {
	n := len(m)
	baseKV, baseOrd, baseU64 = s.ReserveMapKVScalar(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	u64Slice := s.KVScalar[baseU64 : baseU64+n]
	for k, v := range m {
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			u64Slice[count] = uint64(v)
		} else {
			s.GrowMapKVScalarOne(k, uint64(v), count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			u64Slice = s.KVScalar[baseU64:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVScalar(baseKV, n, baseOrd, baseU64)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVScalar(baseKV, baseOrd, baseU64, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseU64, count
}

func sortStrU64Map(m map[string]uint64, s *vars.Stack) (baseKV int, baseOrd int, baseU64 int, count int) {
	n := len(m)
	baseKV, baseOrd, baseU64 = s.ReserveMapKVScalar(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	u64Slice := s.KVScalar[baseU64 : baseU64+n]
	for k, v := range m {
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			u64Slice[count] = v
		} else {
			s.GrowMapKVScalarOne(k, v, count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			u64Slice = s.KVScalar[baseU64:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVScalar(baseKV, n, baseOrd, baseU64)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVScalar(baseKV, baseOrd, baseU64, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseU64, count
}

func sortStrF64Map(m map[string]float64, s *vars.Stack) (baseKV int, baseOrd int, baseU64 int, count int) {
	n := len(m)
	baseKV, baseOrd, baseU64 = s.ReserveMapKVScalar(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	u64Slice := s.KVScalar[baseU64 : baseU64+n]
	for k, v := range m {
		bits := math.Float64bits(v)
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			u64Slice[count] = bits
		} else {
			s.GrowMapKVScalarOne(k, bits, count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			u64Slice = s.KVScalar[baseU64:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVScalar(baseKV, n, baseOrd, baseU64)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVScalar(baseKV, baseOrd, baseU64, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseU64, count
}

func sortStrBoolMap(m map[string]bool, s *vars.Stack) (baseKV int, baseOrd int, baseU64 int, count int) {
	n := len(m)
	baseKV, baseOrd, baseU64 = s.ReserveMapKVScalar(n)
	kvSlice := s.KVBuf[baseKV : baseKV+n]
	ordSlice := s.KVOrder[baseOrd : baseOrd+n]
	u64Slice := s.KVScalar[baseU64 : baseU64+n]
	for k, v := range m {
		var bv uint64
		if v {
			bv = 1
		}
		if count < len(kvSlice) {
			kvSlice[count] = vars.MapKVEntry{K: k}
			ordSlice[count] = count
			u64Slice[count] = bv
		} else {
			s.GrowMapKVScalarOne(k, bv, count)
			kvSlice = s.KVBuf[baseKV:]
			ordSlice = s.KVOrder[baseOrd:]
			u64Slice = s.KVScalar[baseU64:]
		}
		count++
	}
	if count == 0 {
		s.ReleaseMapKVScalar(baseKV, n, baseOrd, baseU64)
		return 0, 0, 0, 0
	}
	if count < n {
		kvSlice, ordSlice = s.TrimMapKVScalar(baseKV, baseOrd, baseU64, count)
	}
	alg.SortMapKVOrder(ordSlice, kvSlice)
	return baseKV, baseOrd, baseU64, count
}

func encodeStringStringMap(b *[]byte, m map[string]string, s *vars.Stack, flags uint64) {
	sanitize := flags & alg.SanitizeMask
	if flags&(1<<alg.BitSortMapKeys) != 0 {
		if len(m) <= 1 {
			first := true
			for k, v := range m {
				if !first {
					*b = append(*b, ',')
				}
				first = false
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				*b = alg.QuoteFlags(*b, v, false, sanitize)
			}
			return
		}
		baseKV, baseOrd, baseStr, count := sortStrStringMap(m, s)
		if count == 0 {
			return
		}
		for idx := 0; idx < count; idx++ {
			if idx > 0 {
				*b = append(*b, ',')
			}
			ord := s.KVOrder[baseOrd+idx]
			k := s.KVBuf[baseKV+ord].K
			v := s.KVStr[baseStr+ord]
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			*b = alg.QuoteFlags(*b, v, false, sanitize)
		}
		s.ReleaseMapKVStr(baseKV, count, baseOrd, baseStr)
		return
	}
	first := true
	if sanitize == 0 {
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKey(*b, k)
			*b = alg.Quote(*b, v, false)
		}
		return
	}
	for k, v := range m {
		if !first {
			*b = append(*b, ',')
		}
		first = false
		*b = alg.QuoteKeyFlags(*b, k, sanitize)
		*b = alg.QuoteFlags(*b, v, false, sanitize)
	}
}

//go:noinline
func encodeBuiltinMap(b *[]byte, p unsafe.Pointer, kind int, s *vars.Stack, flags uint64) error {
	sanitize := flags & alg.SanitizeMask
	sorted := flags&(1<<alg.BitSortMapKeys) != 0
	switch kind {
	case vars.MapBuiltinStrStr:
		encodeStringStringMap(b, *(*map[string]string)(p), s, flags)
		return nil
	case vars.MapBuiltinStrInt:
		m := *(*map[string]int)(p)
		if sorted && len(m) > 1 {
			baseKV, baseOrd, baseU64, count := sortStrIntMap(m, s)
			if count == 0 {
				return nil
			}
			for idx := 0; idx < count; idx++ {
				if idx > 0 {
					*b = append(*b, ',')
				}
				ord := s.KVOrder[baseOrd+idx]
				k := s.KVBuf[baseKV+ord].K
				v := int64(s.KVScalar[baseU64+ord])
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				*b = alg.I64toa(*b, v)
			}
			s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
			return nil
		}
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			*b = alg.I64toa(*b, int64(v))
		}
		return nil
	case vars.MapBuiltinStrI64:
		m := *(*map[string]int64)(p)
		if sorted && len(m) > 1 {
			baseKV, baseOrd, baseU64, count := sortStrI64Map(m, s)
			if count == 0 {
				return nil
			}
			for idx := 0; idx < count; idx++ {
				if idx > 0 {
					*b = append(*b, ',')
				}
				ord := s.KVOrder[baseOrd+idx]
				k := s.KVBuf[baseKV+ord].K
				v := int64(s.KVScalar[baseU64+ord])
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				*b = alg.I64toa(*b, v)
			}
			s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
			return nil
		}
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			*b = alg.I64toa(*b, v)
		}
		return nil
	case vars.MapBuiltinStrU64:
		m := *(*map[string]uint64)(p)
		if sorted && len(m) > 1 {
			baseKV, baseOrd, baseU64, count := sortStrU64Map(m, s)
			if count == 0 {
				return nil
			}
			for idx := 0; idx < count; idx++ {
				if idx > 0 {
					*b = append(*b, ',')
				}
				ord := s.KVOrder[baseOrd+idx]
				k := s.KVBuf[baseKV+ord].K
				v := s.KVScalar[baseU64+ord]
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				*b = alg.U64toa(*b, v)
			}
			s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
			return nil
		}
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			*b = alg.U64toa(*b, v)
		}
		return nil
	case vars.MapBuiltinStrF64:
		m := *(*map[string]float64)(p)
		nullInfNan := flags&(1<<alg.BitEncodeNullForInfOrNan) != 0
		if sorted && len(m) > 1 {
			baseKV, baseOrd, baseU64, count := sortStrF64Map(m, s)
			if count == 0 {
				return nil
			}
			for idx := 0; idx < count; idx++ {
				if idx > 0 {
					*b = append(*b, ',')
				}
				ord := s.KVOrder[baseOrd+idx]
				k := s.KVBuf[baseKV+ord].K
				v := math.Float64frombits(s.KVScalar[baseU64+ord])
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				if math.IsNaN(v) || math.IsInf(v, 0) {
					if !nullInfNan {
						s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
						return vars.ERR_nan_or_infinite
					}
					*b = append(*b, 'n', 'u', 'l', 'l')
				} else {
					*b = alg.F64toa(*b, v)
				}
			}
			s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
			return nil
		}
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			if math.IsNaN(v) || math.IsInf(v, 0) {
				if !nullInfNan {
					return vars.ERR_nan_or_infinite
				}
				*b = append(*b, 'n', 'u', 'l', 'l')
			} else {
				*b = alg.F64toa(*b, v)
			}
		}
		return nil
	case vars.MapBuiltinStrBool:
		m := *(*map[string]bool)(p)
		if sorted && len(m) > 1 {
			baseKV, baseOrd, baseU64, count := sortStrBoolMap(m, s)
			if count == 0 {
				return nil
			}
			for idx := 0; idx < count; idx++ {
				if idx > 0 {
					*b = append(*b, ',')
				}
				ord := s.KVOrder[baseOrd+idx]
				k := s.KVBuf[baseKV+ord].K
				v := s.KVScalar[baseU64+ord] != 0
				*b = alg.QuoteKeyFlags(*b, k, sanitize)
				if v {
					*b = append(*b, 't', 'r', 'u', 'e')
				} else {
					*b = append(*b, 'f', 'a', 'l', 's', 'e')
				}
			}
			s.ReleaseMapKVScalar(baseKV, count, baseOrd, baseU64)
			return nil
		}
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = alg.QuoteKeyFlags(*b, k, sanitize)
			if v {
				*b = append(*b, 't', 'r', 'u', 'e')
			} else {
				*b = append(*b, 'f', 'a', 'l', 's', 'e')
			}
		}
		return nil
	case vars.MapBuiltinIntStr:
		if sorted {
			return encodeSortedStringElemMapViaIter(b, vars.IntStringMapType, p, flags, sanitize)
		}
		m := *(*map[int]string)(p)
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = append(*b, '"')
			*b = alg.I64toa(*b, int64(k))
			*b = append(*b, '"', ':')
			*b = alg.QuoteFlags(*b, v, false, sanitize)
		}
		return nil
	case vars.MapBuiltinI64Str:
		if sorted {
			return encodeSortedStringElemMapViaIter(b, vars.Int64StringMapType, p, flags, sanitize)
		}
		m := *(*map[int64]string)(p)
		first := true
		for k, v := range m {
			if !first {
				*b = append(*b, ',')
			}
			first = false
			*b = append(*b, '"')
			*b = alg.I64toa(*b, k)
			*b = append(*b, '"', ':')
			*b = alg.QuoteFlags(*b, v, false, sanitize)
		}
		return nil
	}
	return nil
}

func encodeSortedStringElemMapViaIter(b *[]byte, vt reflect.Type, p unsafe.Pointer, flags uint64, sanitize uint64) error {
	it, err := alg.IteratorStart(vt, p, flags)
	if err != nil {
		return err
	}
	first := true
	for it.It.K != nil {
		if !first {
			*b = append(*b, ',')
		}
		first = false
		k := *(*string)(it.It.K)
		v := *(*string)(it.It.V)
		*b = alg.QuoteKeyFlags(*b, k, sanitize)
		*b = alg.QuoteFlags(*b, v, false, sanitize)
		alg.IteratorNext(it)
	}
	alg.IteratorStop(it)
	return nil
}
