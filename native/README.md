# Historical C Reference Sources (Not Built)

The `*.c` and `*.h` files in this directory (along with `scripts/build-x86.sh` and `tools/asm2asm`) are retained **strictly as a historical semantic reference** for C-parity oracle tests and documentation links.

- **Not compiled or linked**: No Go build, `go generate` step, or CI job compiles these C sources or links generated `asm2asm` output.
- **Active native implementation**: All runtime JSON scanning, unquoting, number parsing, quoting, UTF-8 validation, and structural indexing live in pure Go and handwritten Go assembly kernels under:
  - `internal/native/` and `internal/native/kernels/`
  - `internal/decoder/optdec/`
  - `internal/encoder/` and `ast/`
