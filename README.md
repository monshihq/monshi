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

### Storage compatibility

Custom `Storage` implementations must implement `InitDraft` atomically.
Return the existing draft if present; otherwise compare the head to
`initial.Base` and insert revision zero or return `*Stale`. `Save` on a
missing draft now returns `ErrNoDraft`. See [`storage.go`](storage.go) for
the full contract.

New head, draft and history records use wire version 2. Each identifies its
record kind and definition layout. A field rename, enum reorder or stage
reorder is a schema mismatch, including after a restart. Changes limited to
permissions, defaults or decimal constraints do not invalidate the layout.
Version 1 records remain readable under their original layout, but contain
no layout identifier: register the original layout when reopening such data.
Writes upgrade individual records; history is append-only. Version 1 omitted
part or all of the decision/comment for some typed-field reviews. Reading
such an entry returns a storage error rather than inventing an audit record.

Run `go test ./...` for the public API and package tests, or
`go test -race ./...` to include race detection.
