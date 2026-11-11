//go:build !((amd64 && go1.17 && !go1.28) || (arm64 && go1.20 && !go1.28) || (riscv64 && go1.26 && !go1.28 && riscv64.rva22u64))

package optdec

func parseWithSchemaOrFull(p *Parser, _ *projPlan) ErrorCode {
	return p.parse()
}
