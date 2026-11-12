//go:build (amd64 && go1.20 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64)

/*
 * Copyright 2022 ByteDance Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package ast

import (
	"unsafe"

	"github.com/bytedance/sonic/encoder"
	"github.com/bytedance/sonic/internal/encoder/alg"
	"github.com/bytedance/sonic/internal/native"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/utf8"
)

func quote(buf *[]byte, val string) {
	*buf = alg.Quote(*buf, val, false)
}

func quoteKey(buf *[]byte, val string) {
	*buf = alg.QuoteKey(*buf, val)
}

func (self *Parser) decodeValue() (val types.JsonState) {
	flag := types.F_USE_NUMBER
	if self.dbuf != nil {
		flag = 0
		val.Dbuf = self.dbuf
		val.Dcap = types.MaxDigitNums
	}
	self.p = native.Value(unsafe.Pointer(unsafe.StringData(self.s)), len(self.s), self.p, &val, uint64(flag))
	return
}

// decodeKey retains native string validation and cursor handling.
func (self *Parser) decodeKey() (start int, ep int) {
	s := self.s
	p := self.p
	n := len(s)
	if uint(p) < uint(n) && s[p] <= ' ' {
		p = lspaceSlow(s, p)
	}
	if uint(p) < uint(n) && s[p] == '"' {
		p++
		start = p
		limit := start + 16
		if limit > n {
			limit = n
		}
		for p < limit {
			c := s[p]
			if c == '"' {
				self.p = p + 1
				return start, -1
			}
			if c == '\\' {
				break
			}
			p++
		}
		end, escape, err := native.ScanStringRelaxed(s, start-1, p)
		self.p = end
		if err != 0 {
			return -1, -1
		}
		return start, escape
	}
	val := self.decodeValue()
	if val.Vt != types.V_STRING {
		return -1, -1
	}
	return int(val.Iv), val.Ep
}

func (self *Parser) decodeStringTail(start, resumePos int) (Node, types.ParsingError) {
	end, ep, err := native.ScanStringRelaxed(self.s, start-1, resumePos)
	self.p = end
	if err != 0 {
		return Node{}, err
	}
	return self.decodeString(int64(start), ep)
}

func (self *Parser) decodeStringTailStr(start, resumePos int) (string, types.ParsingError) {
	end, ep, err := native.ScanStringRelaxed(self.s, start-1, resumePos)
	self.p = end
	if err != 0 {
		return "", err
	}
	return self.decodeStringStr(int64(start), ep)
}

func (self *Parser) skip() (int, types.ParsingError) {
	start := native.SkipOne(&self.s, &self.p, nil, 0)
	if start < 0 {
		return self.p, types.ParsingError(-start)
	}
	return start, 0
}

func (self *Node) encodeInterface(buf *[]byte) error {
	//WARN: NOT compatible with json.Encoder
	return encoder.EncodeInto(buf, self.packAny(), encoder.NoEncoderNewline)
}

func (self *Parser) skipFast() (int, types.ParsingError) {
	s := self.s
	p := self.p
	n := len(s)
	if uint(p) < uint(n) && s[p] == ' ' && p+1 < n && s[p+1] > ' ' {
		p++
	}
	if uint(p) < uint(n) {
		switch s[p] {
		case '"':
			limit := p + 1 + 16
			if limit > n {
				limit = n
			}
			for i := p + 1; i < limit; i++ {
				b := s[i]
				if b == '"' {
					self.p = i + 1
					return p, 0
				}
				if b == '\\' {
					break
				}
			}
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			limit := p + 1 + 16
			if limit > n {
				limit = n
			}
			i := p + 1
			for i < limit {
				b := s[i]
				if b == ',' || b == '}' || b == ']' || b == ' ' || b == '\n' || b == '\r' || b == '\t' {
					self.p = i
					return p, 0
				}
				i++
			}
			if i == n {
				self.p = n
				return p, 0
			}
		case 't', 'n':
			if n-p >= 4 {
				self.p = p + 4
				return p, 0
			}
		case 'f':
			if n-p >= 5 {
				self.p = p + 5
				return p, 0
			}
		}
	}
	start := native.SkipOneFast(&self.s, &self.p)
	if start < 0 {
		return self.p, types.ParsingError(-start)
	}
	return start, 0
}

func (self *Parser) getByPath(validate bool, path ...interface{}) (int, types.ParsingError) {
	start := native.GetByPathValidate(self.s, &self.p, path, validate)
	if start < 0 {
		return self.p, types.ParsingError(-start)
	}
	return start, 0
}

func validate_utf8(str string) bool {
	return utf8.ValidateString(str)
}

type nativePathPlanHandle = *native.PathPlan

func compilePathPlanImpl(paths [][]interface{}) (nativePathPlanHandle, types.ParsingError) {
	return native.CompilePathPlan(paths)
}

func locateSpansImpl(view inputView, plan nativePathPlanHandle, validate bool, out []locatedSpan) (int, types.ParsingError) {
	var stackNat [16]native.LocatedSpan
	var nat []native.LocatedSpan
	if len(out) <= len(stackNat) {
		nat = stackNat[:len(out)]
	} else {
		nat = make([]native.LocatedSpan, len(out))
	}
	errPos, err := native.LocateMany(view.src, view.base, plan, validate, nat)
	if err != 0 {
		return errPos, err
	}
	for i := range out {
		out[i] = locatedSpan{
			start: nat[i].Start,
			end:   nat[i].End,
			found: nat[i].Found,
		}
	}
	return 0, 0
}

func encodePatchValue(buf *[]byte, val interface{}) error {
	return encoder.EncodeInto(buf, val, encoder.NoEncoderNewline)
}

func validJSONBytes(b []byte) bool {
	ok, _ := encoder.Valid(b)
	return ok
}
