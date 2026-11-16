# Repository Guidelines

## Project Structure & Module Organization

Sonic is a Go JSON library accelerated by handwritten Plan9 SIMD assembly kernels, multi-path adaptive Go scanners (`OptDec`), and a table-driven encoder `VM` with zero `//go:linkname` dependencies. Public APIs live at the repository root and in `ast/`, `decoder/`, `encoder/`, `option/`, `unquote/`, and `utf8/`. Implementation details live under `internal/`, including CPU feature detection (`internal/cpu`), multi-architecture SIMD kernels (`internal/native/`), runtime helpers (`internal/rt/`), caching, and encoder/decoder internals (`internal/encoder/`, `internal/decoder/`). The workspace in `go.work` includes the root module plus `compatibility/`, `external_jsonlib_test/`, `fuzz/`, `generic_test/`, and `issue_test/`. Test data and examples live in `testdata/`, `examples/`, `fuzz/corpus/`, and package-local `*_test.go` files.

## Build, Test, and Development Commands

- `go test ./...`: run the main module tests.
- `SONIC_MODE=noavx2 go test ./...` / `SONIC_MODE=nossse3 go test ./...` (on `amd64`) or `SONIC_MODE=fallback go test ./...` (on `arm64`/`riscv64`): exercise scalar/lower-ISA fallback paths.
- `go test ./issue_test` and `go test ./generic_test`: run regression and compatibility suites from the workspace.
- `go test ./compatibility`: run the Go 1.27 `encoding/json` semantic drift catalog (see `docs/sonic-go127-compatibility.md`).
- `./scripts/test_race.sh`: run the expected race-detection regression check.
- `./scripts/fuzz.sh fuzz`, then `./scripts/fuzz.sh run`: initialize and run fuzzing.
- `./scripts/bench.sh`: run core encoder, decoder, AST, and external JSON benchmark suites.

## Coding Style & Naming Conventions

Use `gofmt` for all Go files and follow Go Code Review Comments. Do not introduce `//go:linkname` or private Go runtime layout assumptions. Go tests use `TestXxx`, `BenchmarkXxx`, and `FuzzXxx`. Issue regressions follow `issue_test/issueNNN_test.go`. Shell scripts should be executable, use bash consistently, and keep environment toggles explicit. Write repository documentation in English by default; use another language only when the target audience or existing document explicitly requires it.

## Testing Guidelines

Add focused package tests beside changed Go code. For parser, encoder, or SIMD kernel changes, verify native and scalar/lower-ISA modes (`SONIC_MODE`) as well as cross-architecture `go vet`. For user-reported behavior fixes, add or update an `issue_test` case. For fuzz-sensitive parsing changes, run `FuzzFiveWayDifferential` in `./internal/decoder/optdec` and seed `fuzz/corpus/` or `fuzz/testdata/` when useful.

## Commit & Pull Request Guidelines

History and the PR template use Conventional Commits, for example `feat: support Go 1.26`, `fix(decoder): handle ...`, `opt: ast node set`, `ci: add ...`, or `chore: update go mod`. Branch names should use prefixes such as `feature/`, `bugfix/`, `optimize/`, `doc/`, `ci/`, `test/`, or `refactor/`; validate with `./scripts/check_branch_name.sh`. PRs should describe user-visible impact, link issues when applicable, include tests or benchmark data for performance changes, and update user documentation when behavior changes.

## Security & Compatibility Notes

Do not disclose security bugs in public issues; follow `CONTRIBUTING.md` and contact the maintainers. Supported Go versions and behavioral notes are documented in `README.md` and `COMPATIBILITY.md`.
