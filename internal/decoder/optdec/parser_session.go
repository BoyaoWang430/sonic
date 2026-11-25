//go:build !sonic_compat && ((amd64 && go1.20) || (arm64 && go1.20) || (riscv64 && go1.26 && riscv64.rva22u64) || (sonic_scalar && go1.20 && (amd64 || arm64 || riscv64)))

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

package optdec

import (
	"fmt"
	"runtime"

	"github.com/bytedance/sonic/internal/jsonscan"
	"github.com/bytedance/sonic/internal/native/types"
	"github.com/bytedance/sonic/internal/rt"
	"github.com/bytedance/sonic/utf8"
)

// InputSession caches UTF-8 validation state across consecutive multi-value
// Decode calls on the same string Decoder, eliminating repeated UTF-8 full-tail
// scans with zero full-input copies (CL12).
type InputSession struct {
	src         string
	base        int
	utf8Checked bool
}

func hasMoreJSONTokens(s string, pos int) bool {
	for pos < len(s) {
		c := s[pos]
		if c > ' ' || (types.SPACE_MASK&(1<<c)) == 0 {
			return true
		}
		pos++
	}
	return false
}

// newParserImpl initializes a read-only zero-copy Parser over data[pos:] without
// copying the input into a full-document padded buffer (CL11 / CL12).
func newParserImpl(data string, pos int, opt uint64) *Parser {
	return newParserLazy(data, pos, opt)
}

// initParserLazy initializes an already-allocated Parser p over data[pos:] without
// copying the input. Shared by newParserLazy (pooled) and test pinnedParser injection.
func initParserLazy(p *Parser, data string, pos int, opt uint64) *Parser {
	if pos < 0 {
		pos = 0
	} else if pos > len(data) {
		pos = len(data)
	}
	p.Json = data[pos:]
	if opt&(1<<_F_validate_string) != 0 && !utf8.ValidateString(p.Json) {
		dbuf := utf8.CorrectWith(nil, rt.Str2Mem(p.Json), "\ufffd")
		p.Json = rt.Mem2Str(dbuf)
		p.Utf8Inv = true
	}
	p.pos = 0
	p.end = len(p.Json)
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

// newParserLazy initializes a Parser over data[pos:] without copying the input.
func newParserLazy(data string, pos int, opt uint64) *Parser {
	p := parsePool.Get().(*Parser)
	return initParserLazy(p, data, pos, opt)
}

// newSubParserPreValidated borrows a pooled Parser bounded to fullSrc[rawStart:rawEnd]
// for an already UTF-8-validated/repaired source slice without re-running UTF-8
// validation/repair or scanning trailing bytes beyond rawEnd (R2 / C08, §5.6).
func newSubParserPreValidated(fullSrc string, rawStart, rawEnd int, opt uint64, utf8Inv bool) *Parser {
	p := parsePool.Get().(*Parser)
	p.Json = fullSrc[:rawEnd]
	p.pos = rawStart
	p.end = rawEnd
	p.Utf8Inv = utf8Inv
	p.options = opt
	p.nbuf.init(p.nodes)
	return p
}

func newParserFromSession(data string, pos int, opt uint64, sess *InputSession) *Parser {
	return newParserLazyFromSession(data, pos, opt, sess)
}

func newParserLazyFromSession(data string, pos int, opt uint64, sess *InputSession) *Parser {
	if sess != nil && sess.src == data && pos >= sess.base && pos <= len(data) &&
		((opt&(1<<_F_validate_string)) == 0 || sess.utf8Checked) {
		p := parsePool.Get().(*Parser)
		p.Json = data[pos:]
		p.pos = 0
		p.end = len(p.Json)
		p.options = opt
		p.nbuf.init(p.nodes)
		return p
	}
	return newParserLazy(data, pos, opt)
}

func parserJSONBytes(p *Parser) []byte {
	return rt.Str2Mem(p.Json)
}

func parserCorrectedSource(p *Parser, original string, pos int) string {
	return original[:pos] + p.Json
}

// nodeTapeGrowth tracks how far growNodeTape has escalated for one scan.
type nodeTapeGrowth uint8

const (
	// growGeometric grows by 4x, clamped to the valid-JSON bound.
	growGeometric nodeTapeGrowth = iota
	// growToHardBound is entered after a clamped growth still ran out.
	growToHardBound
	// growExhausted means the absolute bound was already reached.
	growExhausted
)

// growNodeTape grows the node tape after the scanner filled it and reports
// whether the scan may be resumed.
func (p *Parser) growNodeTape(stage *nodeTapeGrowth) bool {
	curLen := p.nbuf.nlen
	if curLen != len(p.nodes) {
		panic(fmt.Sprintf("current len: %d, real len: %d cap: %d", curLen, len(p.nodes), cap(p.nodes)))
	}
	rem := len(p.Json) - p.pos
	if rem < 0 {
		rem = 0
	}
	var nextCap int
	switch *stage {
	case growGeometric:
		validCap := curLen + calMaxNodeCap(rem)
		nextCap = curLen * 4
		if nextCap < 64 {
			nextCap = 64
		}
		if nextCap > validCap {
			nextCap = validCap
			*stage = growToHardBound
		}
	case growToHardBound:
		nextCap = curLen + rem + 2
		*stage = growExhausted
	default:
		return false
	}
	p.replaceNodesBacking(nextCap, curLen)
	return true
}

func parseRunsGrowing(p *Parser) ErrorCode {
	return parseRunsGrowingMode(p, false)
}

func parseRunsGrowingMode(p *Parser, forceScalar bool) ErrorCode {
	return parseRunsGrowingModeRole(p, forceScalar, jsonscan.DiagRoleMain)
}

func parseRunsGrowingModeRole(p *Parser, forceScalar bool, diagRole uint8) ErrorCode {
	err := parseSharedJSONScanRole(p, nil, forceScalar, diagRole)
	runtime.KeepAlive(p)
	return err
}

func parseImpl(p *Parser) ErrorCode {
	return parseRunsGrowingMode(p, false)
}
