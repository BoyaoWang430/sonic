//go:build (amd64 && go1.16 && !go1.28) || (arm64 && go1.20 && !go1.28)

package alg

import (
	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/rt"
)

func F64toa(buf []byte, v float64) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	buf = rt.GuardSlice2(buf, 64)
	ret := native.F64toa((*byte)(rt.IndexByte(buf, len(buf))), v)
	if ret > 0 {
		return buf[:len(buf)+ret]
	} else {
		return buf
	}
}

func F32toa(buf []byte, v float32) []byte {
	if v == 0 {
		return append(buf, '0')
	}
	buf = rt.GuardSlice2(buf, 64)
	ret := native.F32toa((*byte)(rt.IndexByte(buf, len(buf))), v)
	if ret > 0 {
		return buf[:len(buf)+ret]
	} else {
		return buf
	}
}
