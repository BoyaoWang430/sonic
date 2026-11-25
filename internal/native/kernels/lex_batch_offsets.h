// Verified ABI layout & protocol constants for LexBatch assembly kernels.
// Synchronized with internal/native/types/lex_batch.go and verified by TestLexBatchHeaderOffsets.

#define LEX_BLK_SIZE          24
#define LEX_BLK_EVENT_MASK    0
#define LEX_BLK_CLOSE_QUOTE   8
#define LEX_BLK_INSIDE_SPEC   16

#define LEX_CARRY_SIZE        24
#define LEX_CARRY_ODD_SLASH   0
#define LEX_CARRY_IN_STRING   8
#define LEX_CARRY_PREV_SCALAR 16

#define LEX_OUT_SIZE          16
#define LEX_OUT_BLOCKS        0
#define LEX_OUT_BYTES         8

#define LEX_FLAG_REJECT_CTRL  1
