//go:build riscv64 && go1.26 && !go1.28 && riscv64.rva22u64

package native

import (
	"math/bits"

	"github.com/bytedance/sonic/internal/native/riscv"
	"github.com/bytedance/sonic/internal/native/types"
)

// traversalBlock shares an immutable input block between grammar whitespace
// and successive string tokens. Token boundaries do not discard later masks.
type traversalBlock struct {
	base, end        int
	events, nonspace uint64
}

func (b *traversalBlock) load(src string, pos int, validate bool) {
	var width int
	b.events, b.nonspace, width = riscv.TraversalMasks(src, pos, validate)
	b.base, b.end = pos, pos+width
}

func (b *traversalBlock) skipString(src string, p *int, validate bool) types.ParsingError {
	pos := *p
	// Value strings reuse a block already required by grammar or a preceding key.
	// A cache miss keeps the complete scanner instead of eagerly classifying a
	// separate block whose trailing results might never be reused.
	if pos < b.end {
		events := b.events >> uint(pos-b.base)
		if events != 0 {
			pos += bits.TrailingZeros64(events)
			if src[pos] == '"' {
				*p = pos + 1
				return 0
			}
		} else {
			pos = b.end
		}
	}
	// The cached prefix has no escapes or invalid controls. Continue a long
	// string, or process the first exceptional event, in the complete scanner.
	// It retains exact escape/error semantics without re-scanning that prefix.
	end, err := riscv.SkipString(src[pos:], validate)
	*p = pos + end
	return err
}

func (b *traversalBlock) skipKey(src string, p *int, validate bool) types.ParsingError {
	i := *p
	if i < len(src) {
		c := src[i]
		if c == '"' {
			*p = i + 1
			return 0
		}
		if i+1 < len(src) && src[i+1] == '"' && c != '\\' && (!validate || c >= ' ') {
			*p = i + 2
			return 0
		}
	}
	// Keys populate the shared block, then consume its first event directly.
	// Keeping that work together avoids routing the same position through a
	// separate cache loader and the value-string recovery helper.
	var events uint64
	if i >= b.end {
		var width int
		events, b.nonspace, width = riscv.TraversalMasks(src, i, validate)
		b.base, b.end, b.events = i, i+width, events
	} else {
		events = b.events >> uint(i-b.base)
	}
	if events != 0 {
		i += bits.TrailingZeros64(events)
		if src[i] == '"' {
			*p = i + 1
			return 0
		}
	} else {
		i = b.end
	}
	end, err := riscv.SkipString(src[i:], validate)
	*p = i + end
	return err
}
