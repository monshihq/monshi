# Monshi

Monshi compiles workflow definitions and executes cases against them. Hosts
use the root Go package; tools that inspect definition source can use `schema`.

| Package | Where to start |
| --- | --- |
| `monshi` | `load.go` connects parsing to compilation. `runtime.go` registers definitions and opens cases. `case.go` is the host-facing operation API. |
| `schema` | `parser.go` reads the DSL; `check.go` resolves and checks the model in `model.go`. |
| `internal/program` | `lower.go` compiles a checked schema. `image.go` and `query.go` describe the immutable program the engine reads. |
| `internal/engine` | `frame.go` holds live data. Operation files such as `submit.go` and `rebase.go` implement case rules. `commit.go` prepares a commit; `native_store.go` and `storage_adapter.go` publish it through their respective backends. |

The [design](docs/DESIGN.md) defines case behavior, the [syntax
reference](docs/SYNTAX.md) defines the language, and the [runtime
specification](docs/RUNTIME.md) explains execution and its cost model.

Run `go test ./...` for the public API and package tests, or
`go test -race ./...` to include race detection.
