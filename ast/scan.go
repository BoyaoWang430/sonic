/*
 * Copyright 2026 ByteDance Inc.
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
	"strconv"
	"unsafe"

	"github.com/bytedance/sonic/internal/native/number"
	"github.com/bytedance/sonic/internal/native/types"
)

func parseFloat64Fast(s string) (float64, error) {
	n := len(s)
	if n > 0 && n <= 19 {
		i := 0
		neg := false
		if s[0] == '-' {
			neg = true
			i = 1
		}
		if i < n && (n-i == 1 || (s[i] >= '1' && s[i] <= '9')) {
			var u uint64
			for ; i < n; i++ {
				d := s[i] - '0'
				if d > 9 {
					break
				}
				u = u*10 + uint64(d)
			}
			if i == n {
				f := float64(u)
				if neg {
					return -f, nil
				}
				return f, nil
			}
		}
	}
	return number.ParseGoFloat64(s)
}

func parseInt64Fast(s string) (int64, error) {
	n := len(s)
	if n > 0 && n <= 19 {
		i := 0
		neg := false
		if s[0] == '-' {
			neg = true
			i = 1
		}
		if i < n && n-i <= 18 && (n-i == 1 || (s[i] >= '1' && s[i] <= '9')) {
			var u uint64
			for ; i < n; i++ {
				d := s[i] - '0'
				if d > 9 {
					break
				}
				u = u*10 + uint64(d)
			}
			if i == n {
				if neg {
					return -int64(u), nil
				}
				return int64(u), nil
			}
		}
	}
	return strconv.ParseInt(s, 10, 64)
}

func (self *Node) rawInterface(useNumber bool) (interface{}, error) {
	raw := self.toString()
	_, v, errPos, perr, err := parseEagerInterface(raw, useNumber, 0)
	if perr != 0 {
		se := newSyntaxError(SyntaxError{Pos: errPos, Src: raw, Code: perr})
		*self = *se
		return nil, se
	}
	if err != nil {
		return nil, err
	}
	return v, nil
}

func (self *Node) rawMap(useNumber bool) (map[string]interface{}, error) {
	raw := self.toString()
	_, v, errPos, perr, err := parseEagerInterface(raw, useNumber, '{')
	if perr != 0 {
		se := newSyntaxError(SyntaxError{Pos: errPos, Src: raw, Code: perr})
		*self = *se
		return nil, se
	}
	if err != nil {
		return nil, err
	}
	return v.(map[string]interface{}), nil
}

func (self *Node) rawArray(useNumber bool) ([]interface{}, error) {
	raw := self.toString()
	_, v, errPos, perr, err := parseEagerInterface(raw, useNumber, '[')
	if perr != 0 {
		se := newSyntaxError(SyntaxError{Pos: errPos, Src: raw, Code: perr})
		*self = *se
		return nil, se
	}
	if err != nil {
		return nil, err
	}
	return v.([]interface{}), nil
}

// trySetOnRawObject handles Set(key, node) directly on an unshared raw object
// span without eagerly materializing unrelated subtrees. It returns handled=true
// when the raw object transition completed (with or without syntax error).
func (self *Node) trySetOnRawObject(key string, node Node) (exist bool, err error, handled bool) {
	raw := self.toString()
	if len(raw) < 2 || raw[0] != '{' {
		return false, nil, false
	}
	wi := 1
	for wi < len(raw) && (raw[wi] == ' ' || raw[wi] == '\n' || raw[wi] == '\r' || raw[wi] == '\t') {
		wi++
	}
	if wi >= len(raw) {
		return false, nil, false
	}
	if raw[wi] == '}' {
		ti := wi + 1
		for ti < len(raw) && (raw[ti] == ' ' || raw[ti] == '\n' || raw[ti] == '\r' || raw[ti] == '\t') {
			ti++
		}
		if ti == len(raw) {
			lp := new(linkedPairs)
			lp.Push(Pair{Key: key, Value: node})
			*self = Node{t: types.V_OBJECT, l: 1, p: unsafe.Pointer(lp)}
			return false, nil, true
		}
	}
	if raw[wi] != '"' {
		return false, nil, false
	}

	orig := *self
	st := new(parseObjectStack)
	st.parser.s = raw
	st.parser.p = wi
	self.t = _V_OBJECT_LAZY
	self.l = 0
	self.p = unsafe.Pointer(st)
	p, _ := self.skipKeyLazy(key, 0)
	if p != nil && p.t == V_ERROR {
		*self = orig
		return false, &Node{t: p.t, l: p.l, p: p.p, m: p.m}, true
	}
	if !p.Exists() {
		if self.isLazy() {
			if serr := self.skipAllKey(); serr != nil {
				*self = orig
				return false, serr, true
			}
		}
		if self.len() == 0 {
			*self = newObject(new(linkedPairs))
		}
		s := (*linkedPairs)(self.p)
		s.Push(NewPair(key, node))
		self.l++
		return false, nil, true
	}
	*p = node
	return true, nil, true
}

func (self *Node) skipKeyLazy(key string, i int) (*Node, int) {
	parser, stack := self.getParserAndObjectStack()
	ret := &stack.v
	s := parser.s
	p := parser.p
	ns := len(s)
outer:
	for {
		for uint(p) < uint(ns) && (s[p] == ' ' || s[p] == '\n' || s[p] == '\r' || s[p] == '\t') {
			p++
		}
		if uint(p) < uint(ns) && s[p] == '"' {
			kStart := p + 1
			limit := kStart + 24
			if limit > ns {
				limit = ns
			}
			kp := kStart
			for kp < limit {
				c := s[kp]
				if c == '"' {
					if kp+1 < ns && s[kp+1] == ':' {
						vp := kp + 2
						for vp < ns && (s[vp] == ' ' || s[vp] == '\n' || s[vp] == '\r' || s[vp] == '\t') {
							vp++
						}
						if vp < ns {
							vStart := vp
							vc := s[vp]
							vEnd := -1
							var vt types.ValueType
							switch vc {
							case '"':
								vt = types.V_STRING
								vlim := vp + 1 + 48
								if vlim > ns {
									vlim = ns
								}
								for vi := vp + 1; vi < vlim; vi++ {
									b := s[vi]
									if b == '"' {
										vEnd = vi + 1
										break
									}
									if b < 0x20 || b == '\\' {
										break
									}
								}
							case '0':
								vt = _V_NUMBER
								vEnd = vp + 1
							case '1', '2', '3', '4', '5', '6', '7', '8', '9':
								vt = _V_NUMBER
								vlim := vp + 1 + 24
								if vlim > ns {
									vlim = ns
								}
								for vi := vp + 1; vi < vlim; vi++ {
									b := s[vi]
									if b >= '0' && b <= '9' {
										continue
									}
									if b == ',' || b == '}' || b == ' ' || b == '\n' || b == '\r' || b == '\t' {
										vEnd = vi
									}
									break
								}
							case 't':
								if ns-vp >= 4 && s[vp:vp+4] == "true" {
									vt = types.V_TRUE
									vEnd = vp + 4
								}
							case 'f':
								if ns-vp >= 5 && s[vp:vp+5] == "false" {
									vt = types.V_FALSE
									vEnd = vp + 5
								}
							case 'n':
								if ns-vp >= 4 && s[vp:vp+4] == "null" {
									vt = types.V_NULL
									vEnd = vp + 4
								}
							}
							if vEnd >= 0 && vEnd < ns {
								sp := vEnd
								for sp < ns && (s[sp] == ' ' || s[sp] == '\n' || s[sp] == '\r' || s[sp] == '\t') {
									sp++
								}
								if sp < ns {
									sep := s[sp]
									if sep == ',' || sep == '}' {
										pairKey := s[kStart:kp]
										val := newRawNode(s[vStart:vEnd], vt, false)
										ret.push(pairKey, val)
										self.l++
										p = sp + 1
										parser.p = p
										if sep == '}' {
											self.setObject(ret)
											if pairKey == key {
												return &ret.At(ret.size - 1).Value, i
											}
											return nil, -1
										}
										if pairKey == key {
											return &ret.At(ret.size - 1).Value, i
										}
										i++
										continue outer
									}
								}
							}
						}
					}
					break
				}
				if c < 0x20 || c == '\\' {
					break
				}
				kp++
			}
		}
		parser.p = p
		last := self.skipNextPair()
		if last == nil {
			return nil, -1
		}
		if last.Value.t == V_ERROR {
			return &last.Value, -1
		}
		if last.Key == key {
			return &last.Value, i
		}
		if !self.isLazy() {
			return nil, -1
		}
		p = parser.p
		i++
	}
}
