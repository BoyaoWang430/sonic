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

package jsonscan

// APIProfileSpec documents and encodes the 7 orthogonal contract dimensions
// across Sonic's public JSON entrypoints and AST call chains:
//
//  1. RootBoundary:     single-value prefix vs full document vs multi-value stream
//  2. MaxDepth:         StrictMaxContainerDepth (4096) vs CompactMaxContainerDepth (10000)
//  3. StringPolicy:     RejectControl, ValidateEscapes, ValidateUTF8, RejectLoneSurrogate, EscapeHTMLObserve
//  4. OutputMode:       ModeEmit, ModeRawSpan, ModeValidateOnly, ModeCompactObserve, ModePositionSpan
//  5. NumberHandling:   converted target / hint vs raw span (UseNumber) vs validate-only (no range check)
//  6. ErrorMapping:     neutral lexical/grammar facts -> error_adapter public offset/code
//  7. Ownership:        borrowed read-only source vs side Unesc scratch vs user-owned allocation
type RootBoundaryKind uint8

const (
	BoundarySinglePrefix RootBoundaryKind = iota
	BoundaryFullDocument
	BoundaryStreamMultiValue
)

// NumberConsumerKind classifies how an API consumes lexed JSON number spans.
type NumberConsumerKind uint8

const (
	NumberConsumeValidateOnly NumberConsumerKind = iota
	NumberConsumeRawSpan
	NumberConsumeTargetConvert
)

// ASTGrammarScope records whether an AST entrypoint already shares jsonscan,
// can consume shared jsonscan events on an eager sink, or retains lazy ast.Node
// incremental cursor/address invariants.
type ASTGrammarScope string

const (
	ASTScopeNotAST            ASTGrammarScope = "non_ast"
	ASTScopeSharedValidate    ASTGrammarScope = "shared_jsonscan_validate"
	ASTScopeSharedEventSink   ASTGrammarScope = "shared_jsonscan_event_sink"
	ASTScopeEagerSinkEligible ASTGrammarScope = "ast_eager_sink_eligible"
	ASTScopeLazyIncremental   ASTGrammarScope = "ast_lazy_incremental_node"
)

// APIProfileEntry is one auditable row in Sonic's public API & AST boundary matrix.
type APIProfileEntry struct {
	Name           string
	Entrypoint     string
	Boundary       RootBoundaryKind
	MaxDepth       int
	Output         OutputMode
	NumberConsumer NumberConsumerKind
	BorrowsSource  bool
	ASTScope       ASTGrammarScope
	CallChainNote  string
}

// AuthoritativeProfileMatrix returns the authoritative JSON profile & AST call-chain contract table.
func AuthoritativeProfileMatrix() []APIProfileEntry {
	return []APIProfileEntry{
		{
			Name:           "Unmarshal_Default",
			Entrypoint:     "sonic.ConfigDefault.Unmarshal / UnmarshalString",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeEmit,
			NumberConsumer: NumberConsumeTargetConvert,
			BorrowsSource:  false, // CopyString=true by default
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "optdec.Decode -> shared jsonscan 64B LexBlockMask (post-repair <=128MiB ValidatedDoc 8B/entry resumable barrier index + direct binder + lazy 8B/entry ModePositionSpan cold sidecar, or ModeEmit Node Tape + FromDom)",
		},
		{
			Name:           "Unmarshal_UseNumber",
			Entrypoint:     "sonic.Config{UseNumber:true}.Unmarshal",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeEmit,
			NumberConsumer: NumberConsumeRawSpan,
			BorrowsSource:  false,
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "same LexNumber authority as default; consumer_tape emits KRawNumber",
		},
		{
			Name:           "Decoder_Stream",
			Entrypoint:     "Config.NewDecoder(r).Decode",
			Boundary:       BoundaryStreamMultiValue,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeEmit,
			NumberConsumer: NumberConsumeTargetConvert,
			BorrowsSource:  false, // stream buffer may recycle on next Decode
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "StreamSession -> jsonscan.Machine linear chunk resumption + CopyString",
		},
		{
			Name:           "Schema_UnknownSkip",
			Entrypoint:     "optdec.parseSchemaRoot (unknown struct field)",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeValidateOnly,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  false,
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "jsonscan.Machine SubMode=ModeValidateOnly + O(1) StringValidateState (0 B Unesc)",
		},
		{
			Name:           "RawMessage_Subtree",
			Entrypoint:     "json.RawMessage / sonic.NoCopyRawMessage",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeRawSpan,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  true, // NoCopyRawMessage with CopyString=false borrows read-only source
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "jsonscan.Machine SubMode=ModeRawSpan + O(1) StringValidateState (0 B Unesc)",
		},
		{
			Name:           "Valid_Default",
			Entrypoint:     "sonic.Valid / ConfigDefault.Valid / encoder.Valid",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeValidateOnly,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  false,
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "encoder.Valid -> ValidateOne (ASTValidateProfile validateStrings=false) + trailing-whitespace check",
		},
		{
			Name:           "Valid_ConfigStd",
			Entrypoint:     "sonic.ConfigStd.Valid / Config{ValidateString:true}.Valid",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeValidateOnly,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  false,
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "alg.ValidStrictFullDoc -> jsonscan.Machine ModeValidateOnly + StrictProfile (0 B Unesc)",
		},
		{
			Name:           "Compact_Sanitize",
			Entrypoint:     "sonic.Compact / alg.AppendCompactedJSON",
			Boundary:       BoundaryFullDocument,
			MaxDepth:       CompactMaxContainerDepth,
			Output:         ModeCompactObserve,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  false,
			ASTScope:       ASTScopeNotAST,
			CallChainNote:  "jsonscan.Machine ModeCompactObserve + CompactProfile (depth 10000, allows lone surrogates by default)",
		},
		{
			Name:           "AST_Get_Search_PathPlan",
			Entrypoint:     "sonic.Get / Searcher.GetByPath / Searcher.GetManyWithPlan",
			Boundary:       BoundarySinglePrefix,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeValidateOnly,
			NumberConsumer: NumberConsumeValidateOnly,
			BorrowsSource:  true, // CopyReturn=false references input slice; CopyReturn=true clones
			ASTScope:       ASTScopeSharedValidate,
			CallChainNote:  "ast.Searcher -> native.SkipOne / native.LocateSpans -> ValidateOneAtDepth -> jsonscan.Machine (ASTValidateProfile)",
		},
		{
			Name:           "AST_Loads_RawInterface",
			Entrypoint:     "ast.Loads / ast.LoadsUseNumber / (*ast.Node).rawInterface / rawMap / rawArray",
			Boundary:       BoundarySinglePrefix,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeEventSink,
			NumberConsumer: NumberConsumeTargetConvert,
			BorrowsSource:  true,
			ASTScope:       ASTScopeSharedEventSink,
			CallChainNote:  "jsonscan.Machine ModeEventSink + LexBatch -> astInterfaceSink (no OptDec tape, no duplicate container grammar; G4.4b)",
		},
		{
			Name:           "AST_Preorder_Visitor",
			Entrypoint:     "ast.Preorder",
			Boundary:       BoundarySinglePrefix,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeEventSink,
			NumberConsumer: NumberConsumeTargetConvert,
			BorrowsSource:  true,
			ASTScope:       ASTScopeSharedEventSink,
			CallChainNote:  "jsonscan.Machine ModeEventSink + LexBatch -> astVisitorSink (supports VisitOPSkip via EventContainerSkip; G4.4b)",
		},
		{
			Name:           "AST_LazyTree_LoadAll_Patch",
			Entrypoint:     "ast.NewParser / (*ast.Node).Get / Index / Load / LoadAll / Set / Patch",
			Boundary:       BoundarySinglePrefix,
			MaxDepth:       StrictMaxContainerDepth,
			Output:         ModeRawSpan,
			NumberConsumer: NumberConsumeRawSpan,
			BorrowsSource:  true,
			ASTScope:       ASTScopeLazyIncremental,
			CallChainNote:  "ast.Parser decodeObject/decodeArray + native.Value; NOT an OptDec Runs call; preserves *Node address stability, lazy-to-materialized transition, and raw-span Set/Patch",
		},
	}
}
