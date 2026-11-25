//go:build !sonic_compat && !sonic_scalar && riscv64 && go1.26 && !go1.28 && riscv64.rva23u64

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

#include "textflag.h"

// Test-only caller: report the actual E8/M4 vector capacity.
TEXT ·utf8VectorBytes(SB), NOSPLIT, $0-8
	VSETVLI	X0, E8, M4, TA, MA, X10
	MOV	X10, n+0(FP)
	RET
