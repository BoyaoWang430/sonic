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
	"runtime"

	"github.com/bytedance/sonic/internal/decoder/consts"
	"github.com/bytedance/sonic/internal/jsonscan"
)

// schemaScanOutcome distinguishes not-eligible plans from completed projection
// scans and authoritative syntax errors without overloading a bare bool.
type schemaScanOutcome uint8

const (
	schemaOutcomeNotEligible schemaScanOutcome = iota
	schemaOutcomeDone
	schemaOutcomeSyntaxError
)

func parseWithSchemaOrFull(p *Parser, plan *projPlan) ErrorCode {
	return parseWithSchemaOrFullMode(p, plan, false)
}

func parseWithSchemaOrFullMode(p *Parser, plan *projPlan, forceScalar bool) ErrorCode {
	outcome, code := parseSchemaRootMode(p, plan, forceScalar)
	switch outcome {
	case schemaOutcomeDone:
		runtime.KeepAlive(p)
		return SONIC_OK
	case schemaOutcomeSyntaxError:
		runtime.KeepAlive(p)
		return code
	default:
		return parseRunsGrowingMode(p, forceScalar)
	}
}

func growSharedNodes(p *Parser, curLen, remBytes int, stage *nodeTapeGrowth) {
	if remBytes < 0 {
		remBytes = 0
	}
	physCap := physicalNodesCap(p.nodes)
	var nextCap int
	switch *stage {
	case growGeometric:
		nextCap = physCap * 4
		if nextCap < 64 {
			nextCap = 64
		}
		validCap := curLen + calMaxNodeCap(remBytes) + 2
		if nextCap > validCap && validCap >= curLen+64 {
			nextCap = validCap
			*stage = growToHardBound
		}
	default:
		nextCap = curLen + remBytes + 4
		*stage = growExhausted
	}
	if nextCap <= curLen+nodeGuardSlots+1 {
		nextCap = curLen + 64
	}
	p.replaceNodesBacking(nextCap, curLen)
}

// parseSharedJSONScan drives the single shared jsonscan.Machine + LexBatch engine
// for both full ModeEmit decoding (jsPlan == nil) and Schema projection (jsPlan!= nil).
func parseSharedJSONScan(p *Parser, jsPlan *jsonscan.Plan, forceScalar bool) ErrorCode {
	return parseSharedJSONScanRole(p, jsPlan, forceScalar, jsonscan.DiagRoleMain)
}

func parseSharedJSONScanRole(p *Parser, jsPlan *jsonscan.Plan, forceScalar bool, diagRole uint8) ErrorCode {
	if cap(p.nodes) == 0 {
		p.nodes = make([]node, defaultNodesCount, defaultNodesCount)
		p.nbuf.ncap = cap(p.nodes)
	} else if len(p.nodes) != cap(p.nodes) {
		p.nodes = p.nodes[:cap(p.nodes)]
		p.nbuf.ncap = cap(p.nodes)
	}

	effectiveOpts := p.options
	if !p.isEface && !p.keepUseNumber {
		effectiveOpts &^= 1 << _F_use_number
	}
	useNumber := (effectiveOpts & (1 << _F_use_number)) != 0
	validateStr := (effectiveOpts & (1 << _F_validate_string)) != 0
	strictURC := (effectiveOpts & (1 << _F_disable_urc)) != 0
	startPos := 0
	if p.pos > 0 {
		startPos = p.pos
	}

	m := &p.jsMachine
	if p.unescBuf != nil {
		m.Unesc = p.unescBuf[:0]
	} else {
		m.Unesc = nil
	}
	// newParserLazy / InputSession already validated or repaired UTF-8 on p.Json.
	// Explicitly set diagRole AFTER ResetFastEmit so subtree scans are never
	// conflated with main top-level scans.
	m.ResetFastEmit(useNumber, validateStr, strictURC, p.deferFloatOverflow, forceScalar, startPos)
	m.SetDiagRole(diagRole)
	if jsPlan != nil {
		caseSensitive := (effectiveOpts & uint64(consts.OptionCaseSensitive)) != 0
		disUnknown := (Options(effectiveOpts) & OptionDisableUnknown) != 0
		noValidateSkip := (effectiveOpts & (1 << _F_no_validate_json)) != 0
		m.ConfigureSchema(jsPlan, caseSensitive, disUnknown, noValidateSkip)
	}

	fullLen := len(p.Json)
	if p.end >= 0 && p.end < fullLen {
		fullLen = p.end
	}
	src := p.Json[:fullLen]
	stage := growGeometric

	for {
		m.Nodes = nodesAsJSONScanWritable(p.nodes)
		res := m.Step(src, true)
		p.unescBuf = m.Unesc
		if len(m.Unesc) > 0 {
			p.unesc = m.Unesc
		} else {
			p.unesc = nil
		}
		switch res.Reason {
		case jsonscan.StopNeedOutput:
			growSharedNodes(p, res.Produced, fullLen-res.Consumed, &stage)
			continue
		case jsonscan.StopDone:
			p.hasProjHints = jsPlan != nil
			p.errOffsetPlus1 = 0
			p.pos = res.Consumed
			p.end = fullLen
			p.nbuf.nlen = res.Produced
			p.nbuf.ncap = cap(p.nodes)
			p.nbuf.parent = -1
			p.nbuf.depth = 0
			p.nbuf.stat = statFromJSONScan(m.Stat)
			return SONIC_OK
		default:
			errPos := res.Consumed
			switch res.Code {
			case jsonscan.ErrControlChar, jsonscan.ErrInvalidEscape, jsonscan.ErrInvalidEscapeUTF, jsonscan.ErrInvalidNumber:
				errPos = res.ErrorOffset
			}
			p.pos = errPos
			p.end = fullLen
			p.errOffsetPlus1 = res.ErrorOffset + 1
			p.nbuf.nlen = res.Produced
			p.nbuf.ncap = cap(p.nodes)
			p.nbuf.parent = int64(m.Grammar.ParentNodeIdx)
			p.nbuf.depth = uint64(m.Grammar.Depth)
			p.nbuf.stat = statFromJSONScan(m.Stat)
			return mapJSONScanError(res.Code)
		}
	}
}

func parseSchemaRoot(p *Parser, plan *projPlan) (schemaScanOutcome, ErrorCode) {
	return parseSchemaRootMode(p, plan, false)
}

func parseSchemaRootMode(p *Parser, plan *projPlan, forceScalar bool) (schemaScanOutcome, ErrorCode) {
	if plan == nil || plan.jsPlan == nil || plan.kind == projFull || plan.kind == projScalar || plan.kind == projBytes {
		return schemaOutcomeNotEligible, SONIC_OK
	}
	code := parseSharedJSONScan(p, plan.jsPlan, forceScalar)
	if code == SONIC_OK {
		return schemaOutcomeDone, SONIC_OK
	}
	return schemaOutcomeSyntaxError, code
}
