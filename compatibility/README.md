# Go 1.27 JSON compatibility tests

This module records observed semantic differences between Sonic and the JSON
implementations available with Go 1.27 (the `encoding/json` v1 API facade under
its default jsonv2-backed engine vs legacy `GOEXPERIMENT=nojsonv2`, plus the
experimental `encoding/json/v2` defaults). It is a drift catalog rather than a
claim of byte-for-byte stdlib equivalence, and it does not change Sonic's
production behavior.

See [docs/sonic-go127-compatibility.md](../docs/sonic-go127-compatibility.md)
for the case matrix, interpretation rules, and release boundary.

## Files

| File | Responsibility |
|---|---|
| `catalog_test.go` | Sonic versus the active `encoding/json` backend |
| `jsonv2_test.go` | `encoding/json` v1 facade versus experimental `encoding/json/v2` defaults |
| `backend_jsonv1_test.go` | Selects legacy v1 (`GOEXPERIMENT=nojsonv2`) expectations |
| `backend_jsonv2_test.go` | Selects Go 1.27 default-backend expectations |

Primary preset comparisons use `sonic.ConfigStd`; the catalog also includes
custom-config, streaming, and backend-capability cases (such as `CaseSensitive`
and `sonic.APIKind` checks in `catalog_test.go`).

## Run

Compare both `encoding/json` internal backends on the **same** Go 1.27 patch
toolchain (clearing inherited `GOROOT` / `GOEXPERIMENT` variables):

```sh
# Go 1.27 default: encoding/json v1 API backed by jsonv2
env -u GOROOT -u GOEXPERIMENT GOTOOLCHAIN=go1.27.3 \
  go test ./compatibility/ -count=1

# Go 1.27 legacy encoding/json backend (same Go patch version)
env -u GOROOT GOTOOLCHAIN=go1.27.3 GOEXPERIMENT=nojsonv2 \
  go test ./compatibility/ -count=1
```

`compatibility/` is a separate workspace module. A root-level `go test ./...`
does not run it.
