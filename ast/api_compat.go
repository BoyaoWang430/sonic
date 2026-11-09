//go:build ((!amd64 && !arm64) || go1.28 || !go1.17 || (arm64 && !go1.20)) && (!riscv64 || !go1.26 || go1.28 || !riscv64.rva22u64)

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
	"encoding/json"
	"reflect"
	"unicode/utf8"

	"github.com/bytedance/sonic/internal/compat"
	"github.com/bytedance/sonic/internal/native/types"
)

func init() {
	compat.Warn("sonic/ast")
}

func quote(buf *[]byte, val string) {
	quoteString(buf, val)
}

func quoteKey(buf *[]byte, val string) {
	quoteString(buf, val)
	*buf = append(*buf, ':')
}

func (self *Parser) decodeValue() (val types.JsonState) {
	e, v := decodeValue(self.s, self.p, self.dbuf == nil)
	if e < 0 {
		return v
	}
	self.p = e
	return v
}

// decodeKey returns the key's content start and first escape position.
// A negative start means that the next value is not a complete string.
func (self *Parser) decodeKey() (start int, ep int) {
	start = self.p
	if uint(start) >= uint(len(self.s)) || self.s[start] != '"' {
		start = skipBlank(self.s, start)
		if start < 0 || self.s[start] != '"' {
			// A non-string value can advance the cursor before the caller rejects
			// it as a key. Keep decodeValue's behavior on this uncommon path.
			self.decodeValue()
			return -1, -1
		}
	}

	end, ep := skipString(self.s, start)
	if end < 0 {
		return -1, -1
	}
	self.p = end
	return start + 1, ep
}

func (self *Parser) decodeStringTailStr(start, _ int) (string, types.ParsingError) {
	end, ep := skipString(self.s, start-1)
	if end < 0 {
		self.p = len(self.s)
		return "", types.ParsingError(-end)
	}
	self.p = end
	return self.decodeStringStr(int64(start), ep)
}

func (self *Parser) decodeStringTail(start, _ int) (Node, types.ParsingError) {
	end, ep := skipString(self.s, start-1)
	if end < 0 {
		self.p = len(self.s)
		return Node{}, types.ParsingError(-end)
	}
	self.p = end
	return self.decodeString(int64(start), ep)
}

func (self *Parser) skip() (int, types.ParsingError) {
	e, s := skipValue(self.s, self.p)
	if e < 0 {
		return self.p, types.ParsingError(-e)
	}
	self.p = e
	return s, 0
}

func (self *Parser) skipFast() (int, types.ParsingError) {
	e, s := skipValueFast(self.s, self.p)
	if e < 0 {
		return self.p, types.ParsingError(-e)
	}
	self.p = e
	return s, 0
}

func (self *Node) encodeInterface(buf *[]byte) error {
	out, err := json.Marshal(self.packAny())
	if err != nil {
		return err
	}
	*buf = append(*buf, out...)
	return nil
}

func (self *Parser) getByPath(validate bool, path ...interface{}) (int, types.ParsingError) {
	for _, p := range path {
		if idx, ok := p.(int); ok && idx >= 0 {
			if err := self.searchIndex(idx); err != 0 {
				return self.p, err
			}
		} else if key, ok := p.(string); ok {
			if err := self.searchKey(key); err != 0 {
				return self.p, err
			}
		} else {
			panic("path must be either int(>=0) or string")
		}
	}

	var start int
	var e types.ParsingError
	if validate {
		start, e = self.skip()
	} else {
		start, e = self.skipFast()
	}
	if e != 0 {
		return self.p, e
	}
	return start, 0
}

func validate_utf8(str string) bool {
	return utf8.ValidString(str)
}

type nativePathPlanHandle = [][]interface{}

func compilePathPlanImpl(paths [][]interface{}) (nativePathPlanHandle, types.ParsingError) {
	cp := make([][]interface{}, len(paths))
	for i, p := range paths {
		for _, comp := range p {
			switch v := comp.(type) {
			case string:
			case int:
				if v < 0 {
					return nil, types.ERR_UNSUPPORT_TYPE
				}
			default:
				if comp == nil {
					return nil, types.ERR_UNSUPPORT_TYPE
				}
				rv := reflect.ValueOf(comp)
				switch rv.Kind() {
				case reflect.String:
				case reflect.Int:
					if rv.Int() < 0 {
						return nil, types.ERR_UNSUPPORT_TYPE
					}
				default:
					return nil, types.ERR_UNSUPPORT_TYPE
				}
			}
		}
		row := make([]interface{}, len(p))
		copy(row, p)
		cp[i] = row
	}
	return cp, 0
}

func locateSpansImpl(view inputView, plan nativePathPlanHandle, validate bool, out []locatedSpan) (int, types.ParsingError) {
	for i, path := range plan {
		p := Parser{s: view.src, p: view.base}
		ok := true
		for _, comp := range path {
			p.p = p.lspace(p.p)
			if p.p >= len(p.s) {
				return p.p, types.ERR_EOF
			}
			c := p.s[p.p]
			if switchRawType(c) == _V_NONE {
				return p.p, types.ERR_INVALID_CHAR
			}
			rv := reflect.ValueOf(comp)
			if rv.Kind() == reflect.String {
				if c != '{' {
					var e types.ParsingError
					if validate {
						_, e = p.skip()
					} else {
						_, e = p.skipFast()
					}
					if e != 0 {
						return p.p, e
					}
					ok = false
					break
				}
				if err := p.searchKey(rv.String()); err != 0 {
					if err == types.ERR_NOT_FOUND {
						ok = false
						break
					}
					return p.p, err
				}
			} else {
				if c != '[' {
					var e types.ParsingError
					if validate {
						_, e = p.skip()
					} else {
						_, e = p.skipFast()
					}
					if e != 0 {
						return p.p, e
					}
					ok = false
					break
				}
				if err := p.searchIndex(int(rv.Int())); err != 0 {
					if err == types.ERR_NOT_FOUND {
						ok = false
						break
					}
					return p.p, err
				}
			}
		}
		if !ok {
			out[i] = locatedSpan{start: -1, end: -1, found: false}
			continue
		}
		var start int
		var e types.ParsingError
		if validate {
			start, e = p.skip()
		} else {
			start, e = p.skipFast()
		}
		if e != 0 {
			return p.p, e
		}
		end := p.p
		if switchRawType(p.s[start]) == _V_NUMBER {
			end = 1 + backward(p.s, end-1)
		}
		out[i] = locatedSpan{start: start, end: end, found: true}
	}
	return 0, 0
}

func encodePatchValue(buf *[]byte, val interface{}) error {
	b, err := json.Marshal(val)
	if err != nil {
		return err
	}
	*buf = append(*buf, b...)
	return nil
}

func validJSONBytes(b []byte) bool {
	return json.Valid(b)
}
