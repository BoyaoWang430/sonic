//go:build sonic_compat

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

package sonic_fuzz

import (
	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
)

// Under -tags=sonic_compat (encoding/json stdlib adapter), StreamDecoder and
// StreamEncoder alias encoding/json types and do not expose Sonic-only stream
// extension methods (compat backend capability boundary).
func configureFuzzStreamExtensions(_ *decoder.StreamDecoder, _ *encoder.StreamEncoder) {}
