# Monshi — Design Specification

**Status:** Implementation baseline
**Companions:** [SYNTAX.md](SYNTAX.md) — normative DSL syntax and
static semantics; [RUNTIME.md](RUNTIME.md) — runtime representation,
lowering and interpretation
**Language:** Go
**Scope:** Core library; an optional standalone service may be built on
top of it.

## 1. Purpose and principles

Monshi is a general-purpose engine for administrative cases. A compact
declarative DSL defines case forms, roles, permissions, verification,
signatures, and stage transitions. The runtime interprets a compiled
definition and controls edits, submissions, reviews, signatures, and
transitions.

Design principles:

- **Library first.** The Core is not an HTTP server, database, or UI
  framework. A separate Monshi Service may embed the library and supply
  HTTP/WebSocket interfaces and storage implementations.
- **Minimal dependencies and APIs.** Prefer concise, contextual names;
  do not add abstractions until they serve a concrete boundary.
- **Functional domain core.** Prefer explicit inputs, immutable
  definitions, deterministic state transformations, and isolated I/O. An
  application may initialize one engine instance, but domain functions
  must not depend on hidden mutable singleton state.
- **Thin clients.** The runtime supplies effective, permission-filtered
  form views. Clients do not interpret DSL rules.
- **One authoritative history.** Committed data evolves through a
  linear, immutable sequence of forward diffs; the latest committed
  snapshot makes reads fast.
- **No definition versioning or migration framework.** The currently
  loaded schema interprets current data; incompatible structures produce
  errors, not automatic migrations.

## 2. Architecture and ownership

- **Host** — definition sources/IDs, user authentication, primary
  roles, application API or UI, session management, storage
  implementation, hook implementation and delivery.
- **Monshi Core** — DSL compilation, interpretation, access control,
  working-state transformations, validation, view generation, commit
  preparation and deterministic replay.
- **Storage** — case identity and records, committed history, latest
  committed state, per-user working states, atomic revision checks and
  updates.
- **Binary storage** — durable, immutable binary objects, including
  historically referenced versions.
- **Optional Monshi Service** — hosts Core, storage adapters, API and
  optional WebSocket notifications; adds no case-specific business
  logic.

Storage interfaces are supplied by the Host (or the service), not tied
to a particular DB. File-loading utilities are optional; they do not
belong to the Core compiler.

The Host chooses whether the library runs in process or through a
service. The service may ship several storage backends; the library
remains storage-agnostic.

## 3. Definitions and loading

Each definition typically lives in one DSL file. The Host derives its
unique ID from the filename stem and passes that ID explicitly; Core
does not require filesystem access.

Illustrative package-level API:

```go
func Load(id string, source io.Reader) error
func Create(definition string, actor Actor) (Case, error)
func Open(id CaseID, actor Actor) (Case, error)
```

The shipped surface for this section is RUNTIME §10. `Load` is the
package function it looks like, and it hands back the immutable
definition rather than installing it; installing is `Runtime.Register`,
and `Create` and `Open` are methods of that same `Runtime`, which
resolves both by ID through one registry.

A loaded definition is compiled and immutable. Duplicate IDs are
rejected. The Host explicitly loads definitions; Monshi does not watch
files or automatically reload changes. More than one definition can
coexist. `Create` selects the definition by ID; `Open` resolves it
through the case record's stored definition ID.

A definition is **not versioned**. After an explicit replacement/reload,
data is interpreted using the newly loaded definition. Schema
compatibility is checked when necessary (including `View`), with an
error for structural incompatibility. Missing newly required values mean
**incomplete**, not necessarily **incompatible**. Neither old commits
nor existing working data are silently rewritten by a definition change.

## 4. DSL model

A source file contains top-level blocks (no outer `case` block):

```text
types { ... }
roles { ... }
forms { ... }
stages { ... }
```

The language uses braces and semicolons. Names of forms, roles and
stages precede their blocks without a colon; `:` separates field/type
names from their types. The concrete syntax, lexical structure and
static semantics of this language are normatively specified in
[SYNTAX.md](SYNTAX.md).

### 4.1 Types and fields

Built-in types include `string`, `integer`, `decimal(precision, scale)`,
`boolean`, `date`, `datetime`, `binary`, `enum`, `options`, and
`signature`. Custom types can derive from primitive types. A declared
role is also a valid field type: a committed role-typed field names a
user who thereby gains that case-specific role. Role and type names
cannot collide.

Fields and groups can carry `required`, `verifiable`, `multiple`,
`default`, and conditional `present[expression]` /
`required[expression]`, where valid for the declaration. Groups may
nest; forms are top level. **Only top-level forms are independently
submitted.** `multiple` applies to a field or group; `required multiple`
requires at least one element. Instances need stable identities (not
positional indexes), represented as ordinary path segments.

`present` controls structural presence in the effective state. A
condition becoming false removes the affected current data on commit,
without rewriting history. `view` permission—not a separate visibility
attribute—controls disclosure. Defaults are part of interpretation;
their presence does not imply that the values were previously committed.

### 4.2 Verification and signatures

- A `verifiable` field, group or form has an effective review state:
  `PENDING`, `APPROVED`, or `REJECTED`.
- Verification is not inherited. If both a parent and descendant
  declare `verifiable`, the parent's verification takes precedence; the
  compiler warns about the redundant descendant annotation.
- A `verifiable multiple` collection is reviewed as a whole, not per
  member.
- A committed content change to a verifiable unit resets its effective
  review state to `PENDING`. Historical decisions remain in history.
- Review supports approval, rejection with a required comment, and
  revocation. Revocation is restricted to the author of the current
  active review decision.
- Signature fields appear directly in forms. `signature(Role)` permits
  the first eligible holder of that role to sign the given version;
  `signature(assigned_field)` resolves the authorized user from a
  role-typed field.
- Signing binds the signature to the fully submitted, valid form
  content (excluding signature fields). All effective verifiable units
  must be approved. A content change invalidates the effective signature
  but retains the historical record. Multiple signatures are unordered;
  each signature field has at most one effective signature per content
  version.
- Cryptographic and biometric signatures are possible; a printed
  signature image alone is presentational, not proof of authentication.

### 4.3 Roles and permissions

A user has one externally authenticated primary role and any
supplementary case roles assigned through **committed** role-typed
fields. All roles have equal priority; pending working edits cannot
grant authority.

Supported permissions: `view`, `edit`, `submit`, `review`, `sign`,
`accept`, `reject`. Permissions apply to targets except `accept` and
`reject`, which govern stage transitions. Default-deny; only positive
grants grant access. Explicit deny overrides allow across all effective
roles and scope inheritance. Permissions inherit Case → Form → Group →
Field unless denied.

Illustrative syntax:

```text
roles {
    Applicant {
        allow view, edit application;
        deny edit application.internal;
    }
    Manager {
        allow view all;
    }
}
```

Stage grants include a role name: `allow Manager accept, reject;` or
`deny Applicant edit assessment;`. Target-free and targeted permissions
cannot be mixed in one statement.

### 4.4 Stages and transitions

The **first declared stage** is the initial stage. Stage names denote
administrative phases, not mandatory individual review tasks. Stage
transitions are explicit, user-triggered, and limited to declared edges:

```text
stages {
    entry {
        allow Applicant view, edit, submit application;
        allow Manager accept, reject;
        accept[application.kind == PERSON] personal;
        accept[application.kind == COMPANY] corporate;
        reject correction;
    }
    personal {
        require application;
    }
    corporate {
        require application;
    }
    correction {}
}
```

`accept` checks its condition and the **destination** stage's `require`
invariants. `require application;` means the form is fully valid and
submitted, its effective verification units are approved, and required
signatures are valid. `reject` bypasses destination completeness
requirements, but still requires its own permission, edge and condition.
Rejection of a stage is not necessarily closure or final rejection of
the case. Multiple conditional paths are permitted; the caller selects
the intended destination. A stage without outgoing edges is terminal. No
undeclared transitions are allowed.

Expressions support Boolean operators, comparisons, parentheses, paths
and literals. No functions are called inside DSL expressions; external
normalization, validation and dynamic options are hooks.

### 4.5 Compiler

A lexer and recursive-descent parser produce an AST; semantic analysis
resolves paths/names and types, checks duplicate symbols, role/type
conflicts, invalid attributes, incompatible permissions, bad signature
references and invalid transitions. Output is an immutable compiled
definition. Useful warnings include ignored nested verification,
unreachable stages, unused declarations, contradictory grants and
constant conditions. Runtime—not the compiler—validates current case
data.

## 5. Persisted model

Storage persists exactly three kinds of case state:

1. **Commit history:** ordered, immutable commits containing forward
   diffs.
2. **Latest committed state:** a materialized snapshot of the current
   persisted case (data, stage, reviews, signatures and revision).
3. **Working states:** one persisted working copy per `(case, user)`,
   shared by all sessions of that user.

Storage contains **data, not the schema**. The definition is resolved
separately. Working states may be stored in full; no delta-only
optimization is required. History is the authority for committed
changes. A working state is private, uncommitted data.

Conceptual types:

```go
type CaseRecord struct {
    ID         CaseID
    Definition string
    Revision   uint64
    State      CaseState
}

type WorkingState struct {
    BaseRevision    uint64
    WorkingRevision uint64
    Data            CaseData
}

type Commit struct {
    Revision  uint64
    Actor     UserID
    Timestamp time.Time
    Operation Operation
    Changes   []Change
}
```

`CaseState` includes more than form values: current stage, review state
and signature state. The exact Go representation of these types remains
an implementation detail. No definition revision is embedded in a
commit.

**Case creation is not a commit.** `Storage.Create` allocates the ID and
stores an empty case record with `revision = 0`, no history, and an
initial stage determined by the first declared stage. Working state is
first created when the user opens the case. The first successful
committed operation creates revision 1.

### 5.1 Diffs

A commit is a **forward diff**: it records new values/effects, never old
values. Semantic committed changes include field value assignment/
removal, review decisions, signatures and stage changes. For repeated
groups, paths address instances by stable IDs, e.g.
`application.shareholders.42` (no `[id:42]` syntax).

Working edits use four operations with distinct meanings:

```text
Set(path, value)  — set a field's value
Empty(path)       — clear an existing field's value, preserving the field
Add(path)         — create an empty instance in a multiple collection
Remove(path)      — remove an instance of a multiple collection
```

`Add` does **not** take an initial value, and it cannot: the ID the new
instance is addressed by belongs to the engine, so it is only known when
the batch that created it returns (`ChangeResult.Created` names it). The
values go in a following `Update`, presented with the working revision
the first one reported. `Remove` is not for deleting fixed schema nodes.
`Empty` differs from a field being structurally absent because of
`present`: it clears a cell the definition still asks for, so a
`required` field stays required and still fails.

### 5.2 Binary values

Binary content is historically retained just like other data, although
physical storage can be separate. Diffs and snapshots may carry
immutable `BinaryRef` values, while a pluggable binary store holds
bytes. Old committed references must remain retrievable. Binary data
must be durably available **before** any commit references it; orphaned
uncommitted objects may be reclaimed later. Core must not assume a
specific object store or database.

Replay of committed changes must be deterministic; **never rerun hooks
or revalidate old commits** during replay.

## 6. Case API and operation semantics

`Case` is a lightweight value/handle identified by case ID and actor; it
should not own an `engine` pointer or its own independently mutable
state. Prefer package functions and functional transformations over
hidden engine state. A single runtime instance per process is acceptable
for wiring, not as an implicit domain dependency. A Go
`context.Context` is distinct from domain `CaseContext`.

The shipped handle carries one pointer, to the wiring that made it: the
registry and the store, both shared, both written only under their own
locks. That is the paragraph's allowance read literally — the handle
owns no state a caller can mutate, and `Update` returns a new working
revision rather than editing anything the handle holds — and it is why
`Open` needs no global to resolve a case through its definition. What
would break the rule is a `Case` holding a frame or a cached snapshot,
which it does not: every operation reads the store afresh. RUNTIME §10
is the shipped surface; the block below stays the semantic one.

Illustrative API (exact Go types and method/function presentation to be
finalized):

```go
// Top level
Load(id string, source io.Reader) error
Create(definition string, actor Actor) (Case, error)
Open(id CaseID, actor Actor) (Case, error)

// Case-scoped
View(paths ...string) ([]ViewNode, error)
Update(changes ...Edit) (ChangeResult, error)
Submit(form string, workingRevision uint64) (CommitResult, error)
Review(path string, decision Decision, revision uint64,
    comment string) (CommitResult, error)
Sign(path string, revision uint64) (CommitResult, error)
Transition(target string, revision uint64) (CommitResult, error)
Rebase() error
Discard() error
History(cursor uint64, limit int) ([]Commit, error)
```

Actual functions performing I/O should accept Go's `context.Context`
for cancellation/deadlines. These are semantic signatures, not a
requirement to expose stateful receiver methods.

### 6.1 View

`View(paths...)` accepts **multiple paths**, returns each selected
subtree (including descendants), merges overlapping selections, and uses
one consistent working snapshot. With no paths, it returns the entire
permission-filtered case view.

A unified view contains both structural/form metadata and dynamic data:
effective permissions, values, presence, requiredness,
verification/signature state, validation errors, dynamic options and
children. View rendering must enforce `view` permissions at every node.
Clients need no copy of the DSL.

Dynamic `Options` hooks are called by the engine when a requested view
needs the options—not through a separate client `GetOptions` API.

### 6.2 Update

`Update` accepts **multiple edits atomically** against the user's
server-side working state. It does not create a historical commit. All
edits are applied to a candidate state before context-dependent
validation so changes such as province and city are evaluated together.

Structural errors, forbidden edits and hook execution failures reject
the whole update. Ordinary validation errors may be retained with draft
values for UI feedback. Normalization uses `Validate` hooks (there is no
separate `Normalize` hook). Successful responses contain the new
**working revision**, paths whose *view* may have changed (`affected`),
newly created paths, and validation errors. `affected` includes changes
to options, permissions, presence or validation—not just value changes.

Updates should use an expected working revision so concurrent sessions
of the same user cannot silently overwrite one another.

### 6.3 Submit

```text
Submit(form, workingRevision)
```

The client sends **only a top-level form identifier and its working
revision**. The server already holds the working data. The revision
supplied to `Submit` is that of the **working copy the form lives in** —
one per `(case, user)`, per §5 — and it is distinct from the case-wide
committed revision. Checking the copy's revision covers every form in it:
an edit to any form the same actor holds, including the submitted one,
has moved it.

Submit checks: the working copy's expected revision,
`base_revision == current committed revision`, permission,
required/present fields, and final validation using current
rules/hooks. It computes a diff limited to the selected form, atomically
appends a commit and updates the committed snapshot. Other forms'
unsubmitted working edits remain intact; working state is rebased to the
new committed revision after successful submit. Verification resets and
signature invalidations caused by a committed content change are part of
the commit. Submit does **not** move the stage. No-op submit should not
write a meaningless commit.

The storage transaction must make committed-history append,
committed-snapshot update, and the submitting user's working-state
update consistent. If state changes concurrently, reject rather than
apply a stale prepared diff.

### 6.4 Review, Sign and Transition

These operate on **committed** state, not drafts. Each call includes the
**global committed case revision** that the user was shown. If any other
commit has advanced the case revision, reject—even if it changed an
apparently unrelated form.

This is intentional: the user may assess the *whole case* before
approving or signing a particular form. Signing signifies the user's
assertion that they inspected the information; enforcing display is the
UI/user's responsibility, while the engine guarantees revision
correspondence.

- `Review`: target must be verifiable; authorized reviewer may approve,
  reject with comment, or revoke their own current active decision.
- `Sign`: signed form must be fully submitted/valid with approved
  applicable verification units; enforce the signature field's
  authorized user/role, bind to form content and the observed global
  revision.
- `Transition`: choose an explicitly declared target; evaluate
  authorization and conditions; `accept` checks destination `require`;
  `reject` skips completeness checks.

Each successful operation produces an atomic commit and an `affected`
path set so the client can refresh only pertinent subtrees.

### 6.5 Refresh, rebase, discard

Committed changes made by other users **never automatically rewrite
existing working states**. Hosts may notify users via post-commit hooks.
A stale working state remains editable but **cannot submit until
synchronized**.

- `Rebase` is **optional for the user**, but synchronization is
  mandatory before a subsequent submit. It attempts to retain
  unsubmitted edits against the latest committed state. Core owns
  semantic validation; storage coordinates revisions. A failed/conflicting
  rebase leaves the previous working state intact.
- `Discard` drops the user's working copy. A new working copy starts
  from the latest committed state on next access. It is not a historical
  commit. Discard must check the expected working revision to protect
  another session's concurrent edits.

Detailed automatic merge rules and conflict presentation are **not**
part of the initial implementation contract; no implicit merge occurs
during Submit.

## 7. CaseContext and hooks

`CaseContext` is a **read-only, stable snapshot view** used by hooks. It
provides access to case identity, actor, stage, values, reviews,
signatures and effective roles without giving hooks mutation or storage
privileges. It is a logical interface, not necessarily a copy of the
state.

Keep hook contracts separate:

```text
Validate(context, path, value) -> Valid(normalizedValue)
                               | Invalid(errors)
                               | Failure(error)
Options(context, path)         -> options | Failure(error)
OnCommit(context, commit)      -> observer result
```

`Validate` performs both validation and normalization; any normalized
value becomes the value used for subsequent checks and eventual commit.
It may update only its own field value, not unrelated fields. A
read-only candidate-state context lets hooks consider dependencies. For
stable evaluation, dependencies/conditions must be reevaluated after
normalization; validation at Submit runs again.

`Options` is looked up by field path; `Validate` is looked up by custom
type. The DSL contains no calls to these hooks. An execution failure is
an error, not silent success.

A *missing* hook is the case where the choice has to be stated rather
than assumed, because a definition names a hook by declaring an
`options`-typed field or a custom type with a `Validate` rule, and a host
that wired no function for it has not broken its definition — nothing in
the definition is wrong. The rule is therefore per kind: `Options` is
required, since a declared options field with no provider has no list to
show and inventing an empty one would answer a question nobody was able
to ask; `Validate` is absent-means-identity, since its job is to change a
value and the unchanged value is exactly what the type checker already
accepted. Either way the failure is loud: the first is a refusal naming
the binding, and a hook that returns `Failure` refuses the whole batch.

`OnCommit` receives the **actual committed `Commit`**, including its
latest diff and metadata, plus a context representing the **post-commit
state**. It is observer-only, never vetoes/mutates a successful commit,
and must not cause that commit to roll back on failure. The Host/service
owns delivery and retries; durable notification may use an outbox.
Stable commit identity supports idempotent redelivery.

Embedded execution may call hook functions directly; a standalone
service may invoke external providers over RPC. Core should expose one
domain contract irrespective of deployment. External hooks should not be
called inside long-held storage transactions.

The one contract is *where* a hook runs, not *how* it is reached: the
domain half is the signature above plus the read-only context, and a
transport — direct call, RPC, queue — stays in the host's wiring. That is
why an in-process runtime can hold bindings behind a key while a service
resolves the same key against a remote provider, and why no hook result
may be cached across a commit: the context it saw is the snapshot, not a
lease.

## 8. Storage contract

A small Go-oriented interface, with contextual names:

```go
type Storage interface {
    Create(ctx context.Context, definition string) (CaseID, error)
    Load(ctx context.Context, id CaseID) (CaseRecord, error)

    Working(ctx context.Context, id CaseID, user UserID) (WorkingState, error)
    Save(ctx context.Context, id CaseID, user UserID,
        expected uint64, state WorkingState) error
    Discard(ctx context.Context, id CaseID, user UserID,
        expected uint64) error

    Commit(ctx context.Context, tx Transaction) error
    History(ctx context.Context, id CaseID,
        cursor uint64, limit int) ([]Commit, error)
}
```

`Working` finds or initializes the per-user working copy. `Save` is an
optimistic revision-checked working-state write. `Discard` removes it
with a revision guard. `Commit` is a **single atomic compare-and-swap
transaction** that checks the expected committed revision (and, for
Submit, the expected working revision), appends the immutable forward
commit, updates the materialized committed state and updates the
submitting user's working copy if applicable. The domain Core prepares
valid changes but does not choose transaction technology.

A separate binary abstraction can expose `Put(io.Reader) -> BinaryRef`
and `Get(BinaryRef) -> io.ReadCloser` with appropriate request contexts.

Milestone 2 ships a store *behind* this shape rather than this interface
itself: an in-memory seam, unexported, whose methods are named for the
same acts and whose two revision checks are exactly the ones this section
requires.
`NewRuntime` wires it in, because a host has no store to hand over yet.
Milestone 4 supplies the implementation and the seam becomes exported
contract; what does not change then is the operations' shapes, which is
the point of keeping the seam narrow now.

The API may be refined during implementation, but it must not weaken
these atomicity and revision guarantees.

## 9. Errors and guarantees

Use structured errors with stable codes, while allowing
additions/removals as implementation clarifies requirements. Initial
categories:

- `NOT_FOUND`, `ACCESS_DENIED`, `INVALID_OPERATION`, `SCHEMA_MISMATCH`
- `STALE_REVISION`, `CONFLICT`
- `HOOK_FAILURE`, `STORAGE_FAILURE`
- Field-level validation errors: `{path, code, message}`

An operation either performs its allowed atomic state transition or has
no persistent effect. A successful `Update` can still leave invalid
draft field values, whereas `Submit` cannot commit invalid form content.
An `OnCommit` delivery failure cannot invalidate an already durable
commit.

## 10. Implementation order

Keep initial milestones small and focused on runnable behavior:

1. **DSL compiler:** lexer/parser, AST, name/type/path resolution,
   stage/role/permission checking; immutable definitions.
2. **Pure case semantics:** in-memory state, stable paths,
   `Set`/`Empty`/`Add`/`Remove`, conditions, access control, validation
   and diff application.
3. **Views and hooks:** unified subtree `View`, `CaseContext`,
   `Validate` and `Options`, affected-path calculation.
4. **Persistence contract:** simple storage implementation with case
   creation, committed snapshot/history, per-user working state, atomic
   revision-checked commit and immutable binary references.
5. **Workflow operations:** `Submit`, `Review`, `Sign`, `Transition`;
   historical replay, signature invalidation and verification resets.
6. **Synchronization and observers:** explicit `Rebase`/`Discard`,
   `OnCommit` integration and reliable delivery adapter as needed.
7. **Optional service:** API wrapper and WebSocket notifications, using
   the same Core semantics.

### Deliberately deferred

Detailed Rebase conflict resolution, advanced binary garbage
collection, service protocol specifics, storage performance tuning, and
file-system loader utilities. They must not complicate the minimal Core
prematurely.

### Invariants to preserve

1. No working edit appears in committed history before Submit.
2. Every successful committed operation appends exactly one coherent
   forward commit; creating a case appends none.
3. History remains immutable, including historically referenced binary
   content.
4. All sessions of a user address one shared working state for that
   case.
5. Stale working states cannot Submit; stale observed committed
   revisions cannot Review, Sign or Transition.
6. Access control derives from committed role assignments, not draft
   edits.
7. `View` never discloses data without effective `view` permission.
8. Schema changes never silently rewrite persisted data or old commits.
9. Hook execution is read-only with respect to case state; replay never
   invokes hooks.
10. Storage commits are atomic across commit history, latest state and
    the applicable working-state update.
