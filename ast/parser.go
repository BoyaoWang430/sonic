/*
 * Copyright 2021 ByteDance Inc.
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
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/internal/utils"
	"github.com/bytedance/sonic/unquote"
)

const (
	_DEFAULT_NODE_CAP  int = 16
	_APPEND_GROW_SHIFT     = 1
)

const (
	_ERR_NOT_FOUND      types.ParsingError = 33
	_ERR_UNSUPPORT_TYPE types.ParsingError = 34
)

var (
	// ErrNotExist means both key and value doesn't exist
	ErrNotExist error = newError(_ERR_NOT_FOUND, "value not exists")

	// ErrUnsupportType means API on the node is unsupported
	ErrUnsupportType error = newError(_ERR_UNSUPPORT_TYPE, "unsupported type")
)

type Parser struct {
	p         int
	s         string
	noLazy    bool
	noIndex   bool
	loadOnce  bool
	skipValue bool
	dbuf      *byte
	muPool    *linkedPairs
}

/** Parser Private Methods **/

func (self *Parser) delim() types.ParsingError {
	s := self.s
	n := len(s)
	p := self.p
	if p < n && s[p] == ':' {
		self.p = p + 1
		return 0
	}
	p = self.lspace(p)

	/* check for EOF */
	if p >= n {
		return types.ERR_EOF
	}

	/* check for the delimiter */
	if s[p] != ':' {
		return types.ERR_INVALID_CHAR
	}

	/* update the read pointer */
	self.p = p + 1
	return 0
}

func (self *Parser) object() types.ParsingError {
	n := len(self.s)
	p := self.lspace(self.p)

	/* check for EOF */
	if p >= n {
		return types.ERR_EOF
	}

	/* check for the delimiter */
	if self.s[p] != '{' {
		return types.ERR_INVALID_CHAR
	}

	/* update the read pointer */
	self.p = p + 1
	return 0
}

func (self *Parser) array() types.ParsingError {
	n := len(self.s)
	p := self.lspace(self.p)

	/* check for EOF */
	if p >= n {
		return types.ERR_EOF
	}

	/* check for the delimiter */
	if self.s[p] != '[' {
		return types.ERR_INVALID_CHAR
	}

	/* update the read pointer */
	self.p = p + 1
	return 0
}

func (self *Parser) lspace(sp int) int {
	if uint(sp) < uint(len(self.s)) && self.s[sp] > ' ' {
		return sp
	}
	return lspaceSlow(self.s, sp)
}

func lspaceSlow(s string, sp int) int {
	if uint(sp) >= uint(len(s)) {
		return sp
	}
	if p := skipBlank(s, sp); p >= 0 {
		return p
	}
	return len(s)
}

func (self *Parser) backward() {
	for ; self.p >= 0 && utils.IsSpace(self.s[self.p]); self.p -= 1 {
	}
}

func (self *Parser) decodeArray(ret *linkedNodes) (Node, types.ParsingError) {
	sp := self.p
	ns := len(self.s)

	/* check for EOF */
	if self.p = self.lspace(sp); self.p >= ns {
		return Node{}, types.ERR_EOF
	}

	/* check for empty array */
	if self.s[self.p] == ']' {
		self.p++
		return Node{t: types.V_ARRAY}, 0
	}

	/* allocate array space and parse every element */
	for {
		var val Node
		var err types.ParsingError

		if self.skipValue {
			/* skip the value */
			var start int
			if start, err = self.skipFast(); err != 0 {
				return Node{}, err
			}
			if self.p > ns {
				return Node{}, types.ERR_EOF
			}
			t := switchRawType(self.s[start])
			if t == _V_NONE {
				return Node{}, types.ERR_INVALID_CHAR
			}
			val = newRawNode(self.s[start:self.p], t, false)
		} else {
			/* decode the value */
			if val, err = self.Parse(); err != 0 {
				return Node{}, err
			}
		}

		/* add the value to result */
		ret.Push(val)
		self.p = self.lspace(self.p)

		/* check for EOF */
		if self.p >= ns {
			return Node{}, types.ERR_EOF
		}

		/* check for the next character */
		switch self.s[self.p] {
		case ',':
			self.p++
		case ']':
			self.p++
			return newArray(ret), 0
		default:
			// if val.isLazy() {
			//     return newLazyArray(self, ret), 0
			// }
			return Node{}, types.ERR_INVALID_CHAR
		}
	}
}

func (self *Parser) decodeObject(ret *linkedPairs) (Node, types.ParsingError) {
	sp := self.p
	ns := len(self.s)

	/* check for EOF */
	if self.p = self.lspace(sp); self.p >= ns {
		return Node{}, types.ERR_EOF
	}

	/* check for empty object */
	if self.s[self.p] == '}' {
		self.p++
		return Node{t: types.V_OBJECT}, 0
	}

	if self.loadOnce {
		self.muPool = ret
	}

	/* decode each pair */
	for {
		var val Node
		var err types.ParsingError

		/* decode the key */
		start, ep := self.decodeKey()
		if start < 0 {
			return Node{}, types.ERR_INVALID_CHAR
		}

		/* extract the key */
		idx := self.p - 1
		key := self.s[start:idx]

		/* check for escape sequence */
		if ep != -1 {
			if key, err = unquote.String(key); err != 0 {
				return Node{}, err
			}
		}

		/* expect a ':' delimiter */
		if err = self.delim(); err != 0 {
			return Node{}, err
		}

		if self.skipValue {
			/* skip the value */
			var start int
			if start, err = self.skipFast(); err != 0 {
				return Node{}, err
			}
			if self.p > ns {
				return Node{}, types.ERR_EOF
			}
			t := switchRawType(self.s[start])
			if t == _V_NONE {
				return Node{}, types.ERR_INVALID_CHAR
			}
			val = newRawNode(self.s[start:self.p], t, false)
		} else {
			/* decode the value */
			if val, err = self.Parse(); err != 0 {
				return Node{}, err
			}
		}

		/* add the value to result */
		// FIXME: ret's address may change here, thus previous referred node in ret may be invalid !!
		ret.push(key, val)
		self.p = self.lspace(self.p)

		/* check for EOF */
		if self.p >= ns {
			return Node{}, types.ERR_EOF
		}

		/* check for the next character */
		switch self.s[self.p] {
		case ',':
			self.p++
		case '}':
			self.p++
			if self.noIndex {
				return newObjectNoIndex(ret), 0
			}
			return newObject(ret), 0
		default:
			// if val.isLazy() {
			//     return newLazyObject(self, ret), 0
			// }
			return Node{}, types.ERR_INVALID_CHAR
		}
	}
}

func (self *Parser) decodeString(iv int64, ep int) (Node, types.ParsingError) {
	p := self.p - 1
	s := self.s[iv:p]

	/* fast path: no escape sequence */
	if ep == -1 {
		return NewString(s), 0
	}

	/* unquote the string */
	out, err := unquote.String(s)

	/* check for errors */
	if err != 0 {
		return Node{}, err
	} else {
		return NewString(out), 0
	}
}

func (self *Parser) decodeStringStr(iv int64, ep int) (string, types.ParsingError) {
	p := self.p - 1
	s := self.s[iv:p]
	if ep == -1 {
		return s, 0
	}
	return unquote.String(s)
}

/** Parser Interface **/

func (self *Parser) Pos() int {
	return self.p
}

// Parse returns a ast.Node representing the parser's JSON.
// NOTICE: the specific parsing lazy dependens parser's option
// It only parse first layer and first child for Object or Array be default
func (self *Parser) Parse() (Node, types.ParsingError) {
	if self.dbuf == nil {
		s := self.s
		n := len(s)
		p := self.p
		if uint(p) < uint(n) && s[p] <= ' ' {
			if s[p] == ' ' && p+1 < n && s[p+1] > ' ' {
				p++
			} else {
				p = lspaceSlow(s, p)
			}
		}
		if uint(p) < uint(n) {
			c := s[p]
			switch c {
			case '"':
				start := p + 1
				limit := start + 16
				if limit > n {
					limit = n
				}
				i := start
				for i < limit {
					b := s[i]
					if b == '"' {
						self.p = i + 1
						return NewString(s[start:i]), 0
					}
					if b == '\\' {
						break
					}
					i++
				}
				return self.decodeStringTail(start, i)
			case 't':
				if p+4 <= n && s[p+1] == 'r' && s[p+2] == 'u' && s[p+3] == 'e' {
					self.p = p + 4
					return trueNode, 0
				}
			case 'f':
				if p+5 <= n && s[p+1] == 'a' && s[p+2] == 'l' && s[p+3] == 's' && s[p+4] == 'e' {
					self.p = p + 5
					return falseNode, 0
				}
			case 'n':
				if p+4 <= n && s[p+1] == 'u' && s[p+2] == 'l' && s[p+3] == 'l' {
					self.p = p + 4
					return nullNode, 0
				}
			case '{':
				self.p = p + 1
				goto parse_object
			case '[':
				self.p = p + 1
				goto parse_array
			case '0':
				if p+1 == n {
					self.p = p + 1
					return NewNumber(s[p : p+1]), 0
				}
				if p+1 < n {
					d := s[p+1]
					if d == ',' || d == '}' || d == ']' || d == ' ' || d == '\n' || d == '\r' || d == '\t' {
						self.p = p + 1
						return NewNumber(s[p : p+1]), 0
					}
				}
			default:
				if c >= '1' && c <= '9' {
					i := p + 1
					for i < n && s[i] >= '0' && s[i] <= '9' {
						i++
					}
					if i == n {
						self.p = i
						return NewNumber(s[p:i]), 0
					}
					if i < n {
						d := s[i]
						if d == ',' || d == '}' || d == ']' || d == ' ' || d == '\n' || d == '\r' || d == '\t' {
							self.p = i
							return NewNumber(s[p:i]), 0
						}
					}
				}
			}
		}
	}
	switch val := self.decodeValue(); val.Vt {
	case types.V_EOF:
		return Node{}, types.ERR_EOF
	case types.V_NULL:
		return nullNode, 0
	case types.V_TRUE:
		return trueNode, 0
	case types.V_FALSE:
		return falseNode, 0
	case types.V_STRING:
		return self.decodeString(val.Iv, val.Ep)
	case types.V_ARRAY:
		goto parse_array
	case types.V_OBJECT:
		goto parse_object
	case types.V_DOUBLE:
		return NewNumber(self.s[val.Ep:self.p]), 0
	case types.V_INTEGER:
		return NewNumber(self.s[val.Ep:self.p]), 0
	default:
		return Node{}, types.ParsingError(-val.Vt)
	}

parse_array:
	{
		s := self.p - 1
		p := self.p
		if uint(p) < uint(len(self.s)) && self.s[p] == ']' {
			self.p = p + 1
			return Node{t: types.V_ARRAY}, 0
		}
		if self.loadOnce && !self.noLazy {
			self.p = s
			s, e := self.skipFast()
			if e != 0 {
				return Node{}, e
			}
			return self.newRawNode(self.s[s:self.p], types.V_ARRAY, true), 0
		}
		if uint(p) < uint(len(self.s)) && self.s[p] <= ' ' {
			p = skipBlank(self.s, p)
		}
		if p >= self.p && uint(p) < uint(len(self.s)) {
			if self.s[p] == ']' {
				self.p = p + 1
				return Node{t: types.V_ARRAY}, 0
			}
			self.p = p
		}
		if self.noLazy {
			if self.loadOnce {
				self.noLazy = false
			}
			return self.decodeArray(new(linkedNodes))
		}
		return newLazyArray(self), 0
	}

parse_object:
	{
		s := self.p - 1
		p := self.p
		if uint(p) < uint(len(self.s)) && self.s[p] == '}' {
			self.p = p + 1
			return Node{t: types.V_OBJECT}, 0
		}
		if self.loadOnce && !self.noLazy {
			self.p = s
			s, e := self.skipFast()
			if e != 0 {
				return Node{}, e
			}
			return self.newRawNode(self.s[s:self.p], types.V_OBJECT, true), 0
		}
		if uint(p) < uint(len(self.s)) && self.s[p] <= ' ' {
			p = skipBlank(self.s, p)
		}
		if p >= self.p && uint(p) < uint(len(self.s)) {
			if self.s[p] == '}' {
				self.p = p + 1
				return Node{t: types.V_OBJECT}, 0
			}
			self.p = p
		}
		// NOTICE: loadOnce always keep raw json for object or array
		if self.noLazy {
			if self.loadOnce {
				self.noLazy = false
			}
			return self.decodeObject(new(linkedPairs))
		}
		return newLazyObject(self), 0
	}
}

func (self *Parser) searchKey(match string) types.ParsingError {
	ns := len(self.s)
	if err := self.object(); err != 0 {
		return err
	}

	/* check for EOF */
	if self.p = self.lspace(self.p); self.p >= ns {
		return types.ERR_EOF
	}

	/* check for empty object */
	if self.s[self.p] == '}' {
		self.p++
		return _ERR_NOT_FOUND
	}

	var err types.ParsingError
	/* decode each pair */
	for {

		/* decode the key */
		start, ep := self.decodeKey()
		if start < 0 {
			return types.ERR_INVALID_CHAR
		}

		/* extract the key */
		idx := self.p - 1
		key := self.s[start:idx]

		/* check for escape sequence */
		if ep != -1 {
			if key, err = unquote.String(key); err != 0 {
				return err
			}
		}

		/* expect a ':' delimiter */
		if err = self.delim(); err != 0 {
			return err
		}

		/* skip value */
		if key != match {
			if _, err = self.skipFast(); err != 0 {
				return err
			}
		} else {
			return 0
		}

		/* check for EOF */
		self.p = self.lspace(self.p)
		if self.p >= ns {
			return types.ERR_EOF
		}

		/* check for the next character */
		switch self.s[self.p] {
		case ',':
			self.p++
		case '}':
			self.p++
			return _ERR_NOT_FOUND
		default:
			return types.ERR_INVALID_CHAR
		}
	}
}

func (self *Parser) searchIndex(idx int) types.ParsingError {
	ns := len(self.s)
	if err := self.array(); err != 0 {
		return err
	}

	/* check for EOF */
	if self.p = self.lspace(self.p); self.p >= ns {
		return types.ERR_EOF
	}

	/* check for empty array */
	if self.s[self.p] == ']' {
		self.p++
		return _ERR_NOT_FOUND
	}

	var err types.ParsingError
	/* allocate array space and parse every element */
	for i := 0; i < idx; i++ {

		/* decode the value */
		if _, err = self.skipFast(); err != 0 {
			return err
		}

		/* check for EOF */
		self.p = self.lspace(self.p)
		if self.p >= ns {
			return types.ERR_EOF
		}

		/* check for the next character */
		switch self.s[self.p] {
		case ',':
			self.p++
		case ']':
			self.p++
			return _ERR_NOT_FOUND
		default:
			return types.ERR_INVALID_CHAR
		}
	}

	return 0
}

func (self *Node) skipNextNode() *Node {
	if !self.isLazy() {
		return nil
	}

	parser, stack := self.getParserAndArrayStack()
	ret := &stack.v
	sp := parser.p
	ns := len(parser.s)

	/* check for EOF */
	if parser.p = parser.lspace(sp); parser.p >= ns {
		return newSyntaxError(parser.syntaxError(types.ERR_EOF))
	}

	/* check for empty array */
	if parser.s[parser.p] == ']' {
		parser.p++
		self.setArray(ret)
		return nil
	}

	var val Node
	/* skip the value */
	if start, err := parser.skipFast(); err != 0 {
		return newSyntaxError(parser.syntaxError(err))
	} else {
		t := switchRawType(parser.s[start])
		if t == _V_NONE {
			return newSyntaxError(parser.syntaxError(types.ERR_INVALID_CHAR))
		}
		val = newRawNode(parser.s[start:parser.p], t, false)
	}

	/* add the value to result */
	ret.Push(val)
	self.l++
	parser.p = parser.lspace(parser.p)

	/* check for EOF */
	if parser.p >= ns {
		return newSyntaxError(parser.syntaxError(types.ERR_EOF))
	}

	/* check for the next character */
	switch parser.s[parser.p] {
	case ',':
		parser.p++
		return ret.At(ret.Len() - 1)
	case ']':
		parser.p++
		self.setArray(ret)
		return ret.At(ret.Len() - 1)
	default:
		return newSyntaxError(parser.syntaxError(types.ERR_INVALID_CHAR))
	}
}

func (self *Node) skipNextPair() *Pair {
	if !self.isLazy() {
		return nil
	}

	parser, stack := self.getParserAndObjectStack()
	ret := &stack.v
	sp := parser.p
	ns := len(parser.s)

	/* check for EOF */
	if parser.p = parser.lspace(sp); parser.p >= ns {
		return newErrorPair(parser.syntaxError(types.ERR_EOF))
	}

	/* check for empty object */
	if parser.s[parser.p] == '}' {
		parser.p++
		self.setObject(ret)
		return nil
	}

	/* decode one pair */
	var val Node
	var err types.ParsingError

	/* decode the key */
	start, ep := parser.decodeKey()
	if start < 0 {
		return newErrorPair(parser.syntaxError(types.ERR_INVALID_CHAR))
	}

	/* extract the key */
	idx := parser.p - 1
	key := parser.s[start:idx]

	/* check for escape sequence */
	if ep != -1 {
		if key, err = unquote.String(key); err != 0 {
			return newErrorPair(parser.syntaxError(err))
		}
	}

	/* expect a ':' delimiter */
	if err = parser.delim(); err != 0 {
		return newErrorPair(parser.syntaxError(err))
	}

	/* skip the value */
	if start, err := parser.skipFast(); err != 0 {
		return newErrorPair(parser.syntaxError(err))
	} else {
		t := switchRawType(parser.s[start])
		if t == _V_NONE {
			return newErrorPair(parser.syntaxError(types.ERR_INVALID_CHAR))
		}
		val = newRawNode(parser.s[start:parser.p], t, false)
	}

	/* add the value to result */
	ret.push(key, val)
	self.l++
	parser.p = parser.lspace(parser.p)

	/* check for EOF */
	if parser.p >= ns {
		return newErrorPair(parser.syntaxError(types.ERR_EOF))
	}

	/* check for the next character */
	switch parser.s[parser.p] {
	case ',':
		parser.p++
		return ret.At(ret.Len() - 1)
	case '}':
		parser.p++
		self.setObject(ret)
		return ret.At(ret.Len() - 1)
	default:
		return newErrorPair(parser.syntaxError(types.ERR_INVALID_CHAR))
	}
}

/** Parser Factory **/

func (self *Parser) parseInterface(useNumber bool) (interface{}, types.ParsingError, error) {
	if self.dbuf == nil {
		s := self.s
		n := len(s)
		p := self.p
		if uint(p) < uint(n) && s[p] <= ' ' {
			if s[p] == ' ' && p+1 < n && s[p+1] > ' ' {
				p++
			} else {
				p = lspaceSlow(s, p)
			}
		}
		if uint(p) < uint(n) {
			c := s[p]
			switch c {
			case '"':
				start := p + 1
				limit := start + 16
				if limit > n {
					limit = n
				}
				i := start
				for i < limit {
					b := s[i]
					if b == '"' {
						self.p = i + 1
						return s[start:i], 0, nil
					}
					if b == '\\' {
						break
					}
					i++
				}
				str, perr := self.decodeStringTailStr(start, i)
				return str, perr, nil
			case 't':
				if p+4 <= n && s[p+1] == 'r' && s[p+2] == 'u' && s[p+3] == 'e' {
					self.p = p + 4
					return true, 0, nil
				}
			case 'f':
				if p+5 <= n && s[p+1] == 'a' && s[p+2] == 'l' && s[p+3] == 's' && s[p+4] == 'e' {
					self.p = p + 5
					return false, 0, nil
				}
			case 'n':
				if p+4 <= n && s[p+1] == 'u' && s[p+2] == 'l' && s[p+3] == 'l' {
					self.p = p + 4
					return nil, 0, nil
				}
			case '{':
				self.p = p + 1
				return self.decodeObjectInterface(useNumber)
			case '[':
				self.p = p + 1
				return self.decodeArrayInterface(useNumber)
			case '0':
				if p+1 == n || (p+1 < n && (s[p+1] == ',' || s[p+1] == '}' || s[p+1] == ']' || s[p+1] == ' ' || s[p+1] == '\n' || s[p+1] == '\r' || s[p+1] == '\t')) {
					self.p = p + 1
					if useNumber {
						return json.Number(s[p : p+1]), 0, nil
					}
					return float64(0), 0, nil
				}
			default:
				if c >= '1' && c <= '9' {
					u := uint64(c - '0')
					i := p + 1
					for i < n && s[i] >= '0' && s[i] <= '9' {
						u = u*10 + uint64(s[i]-'0')
						i++
					}
					if i == n || (i < n && (s[i] == ',' || s[i] == '}' || s[i] == ']' || s[i] == ' ' || s[i] == '\n' || s[i] == '\r' || s[i] == '\t')) {
						self.p = i
						if useNumber {
							return json.Number(s[p:i]), 0, nil
						}
						if i-p <= 19 {
							return float64(u), 0, nil
						}
						f, err := parseFloat64Fast(s[p:i])
						return f, 0, err
					}
				}
			}
		}
	}
	switch val := self.decodeValue(); val.Vt {
	case types.V_EOF:
		return nil, types.ERR_EOF, nil
	case types.V_NULL:
		return nil, 0, nil
	case types.V_TRUE:
		return true, 0, nil
	case types.V_FALSE:
		return false, 0, nil
	case types.V_STRING:
		str, perr := self.decodeStringStr(val.Iv, val.Ep)
		return str, perr, nil
	case types.V_ARRAY:
		return self.decodeArrayInterface(useNumber)
	case types.V_OBJECT:
		return self.decodeObjectInterface(useNumber)
	case types.V_DOUBLE, types.V_INTEGER:
		numStr := self.s[val.Ep:self.p]
		if useNumber {
			return json.Number(numStr), 0, nil
		}
		f, err := parseFloat64Fast(numStr)
		return f, 0, err
	default:
		return nil, types.ParsingError(-val.Vt), nil
	}
}

func (self *Parser) decodeObjectInterface(useNumber bool) (map[string]interface{}, types.ParsingError, error) {
	sp := self.p
	ns := len(self.s)
	if self.p = self.lspace(sp); self.p >= ns {
		return nil, types.ERR_EOF, nil
	}
	if self.s[self.p] == '}' {
		self.p++
		return map[string]interface{}{}, 0, nil
	}
	ret := make(map[string]interface{})
	for {
		start, ep := self.decodeKey()
		if start < 0 {
			return nil, types.ERR_INVALID_CHAR, nil
		}
		idx := self.p - 1
		key := self.s[start:idx]
		if ep != -1 {
			var perr types.ParsingError
			if key, perr = unquote.String(key); perr != 0 {
				return nil, perr, nil
			}
		}
		if perr := self.delim(); perr != 0 {
			return nil, perr, nil
		}
		val, perr, err := self.parseInterface(useNumber)
		if perr != 0 || err != nil {
			return nil, perr, err
		}
		ret[key] = val
		self.p = self.lspace(self.p)
		if self.p >= ns {
			return nil, types.ERR_EOF, nil
		}
		switch self.s[self.p] {
		case ',':
			self.p++
		case '}':
			self.p++
			return ret, 0, nil
		default:
			return nil, types.ERR_INVALID_CHAR, nil
		}
	}
}

func (self *Parser) decodeArrayInterface(useNumber bool) ([]interface{}, types.ParsingError, error) {
	sp := self.p
	ns := len(self.s)
	if self.p = self.lspace(sp); self.p >= ns {
		return nil, types.ERR_EOF, nil
	}
	if self.s[self.p] == ']' {
		self.p++
		return []interface{}{}, 0, nil
	}
	ret := make([]interface{}, 0, 4)
	for {
		val, perr, err := self.parseInterface(useNumber)
		if perr != 0 || err != nil {
			return nil, perr, err
		}
		ret = append(ret, val)
		self.p = self.lspace(self.p)
		if self.p >= ns {
			return nil, types.ERR_EOF, nil
		}
		switch self.s[self.p] {
		case ',':
			self.p++
		case ']':
			self.p++
			return ret, 0, nil
		default:
			return nil, types.ERR_INVALID_CHAR, nil
		}
	}
}

// Loads parse all json into interface{}
func Loads(src string) (int, interface{}, error) {
	ps := Parser{s: src, noLazy: true, noIndex: true}
	x, perr, err := ps.parseInterface(false)
	if perr != 0 {
		return 0, nil, ps.ExportError(perr)
	}
	if err != nil {
		return 0, nil, err
	}
	return ps.Pos(), x, nil
}

// LoadsUseNumber parse all json into interface{}, with numeric nodes cast to json.Number
func LoadsUseNumber(src string) (int, interface{}, error) {
	ps := Parser{s: src, noLazy: true, noIndex: true}
	x, perr, err := ps.parseInterface(true)
	if perr != 0 {
		return 0, nil, perr
	}
	if err != nil {
		return 0, nil, err
	}
	return ps.Pos(), x, nil
}

// NewParser returns pointer of new allocated parser
func NewParser(src string) *Parser {
	return &Parser{s: src}
}

// NewParser returns new allocated parser
func NewParserObj(src string) Parser {
	return Parser{s: src}
}

// decodeNumber controls if parser decodes the number values instead of skip them
//
//	WARN: once you set decodeNumber(true), please set decodeNumber(false) before you drop the parser
//	otherwise the memory CANNOT be reused
func (self *Parser) decodeNumber(decode bool) {
	if !decode && self.dbuf != nil {
		types.FreeDbuf(self.dbuf)
		self.dbuf = nil
		return
	}
	if decode && self.dbuf == nil {
		self.dbuf = types.NewDbuf()
	}
}

// ExportError converts types.ParsingError to std Error
func (self *Parser) ExportError(err types.ParsingError) error {
	if err == _ERR_NOT_FOUND {
		return ErrNotExist
	}
	return fmt.Errorf("%q", SyntaxError{
		Pos:  self.p,
		Src:  self.s,
		Code: err,
	}.Description())
}

func backward(src string, i int) int {
	for ; i >= 0 && utils.IsSpace(src[i]); i-- {
	}
	return i
}

func (self *Parser) newRawNode(str string, typ types.ValueType, lock bool) Node {
	ret := Node{
		t: typ | _V_RAW,
		p: rt.StrPtr(str),
		l: uint(len(str)),
	}
	if lock {
		if self.muPool != nil {
			ret.m = self.muPool.allocMutex()
		} else {
			ret.m = new(sync.RWMutex)
		}
	}
	return ret
}

func newRawNode(str string, typ types.ValueType, lock bool) Node {
	ret := Node{
		t: typ | _V_RAW,
		p: rt.StrPtr(str),
		l: uint(len(str)),
	}
	if lock {
		ret.m = new(sync.RWMutex)
	}
	return ret
}

var typeJumpTable = [256]types.ValueType{
	'"': types.V_STRING,
	'-': _V_NUMBER,
	'0': _V_NUMBER,
	'1': _V_NUMBER,
	'2': _V_NUMBER,
	'3': _V_NUMBER,
	'4': _V_NUMBER,
	'5': _V_NUMBER,
	'6': _V_NUMBER,
	'7': _V_NUMBER,
	'8': _V_NUMBER,
	'9': _V_NUMBER,
	'[': types.V_ARRAY,
	'f': types.V_FALSE,
	'n': types.V_NULL,
	't': types.V_TRUE,
	'{': types.V_OBJECT,
}

func switchRawType(c byte) types.ValueType {
	return typeJumpTable[c]
}

func (self *Node) loadt() types.ValueType {
	if self.m == nil {
		return self.t
	}
	return (types.ValueType)(atomic.LoadInt64(&self.t))
}

func (self *Node) lock() bool {
	if m := self.m; m != nil {
		m.Lock()
		return true
	}
	return false
}

func (self *Node) unlock() {
	if m := self.m; m != nil {
		m.Unlock()
	}
}

func (self *Node) rlock() bool {
	if m := self.m; m != nil {
		m.RLock()
		return true
	}
	return false
}

func (self *Node) runlock() {
	if m := self.m; m != nil {
		m.RUnlock()
	}
}
