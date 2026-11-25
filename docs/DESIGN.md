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
  The reset is part of the commit that changes the content, not a check
  each operation may remember (RUNTIME §5.6, §5.9), so a `Submit`, a
  `Transition` and a `Review` all clear a unit the same way; the record
  of a decision survives its own erasure.
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
- Invalidation is a fact of state rather than a flag on it: what stands
  beside a signer's name is the revision that signature's own commit
  produced, so the content it bound is the content before that commit, and
  the same commit seam that resets a review erases an attestation whose
  content moved (RUNTIME §5.6, §5.10). A field with nothing stored answers
  one question — no hand stands behind it — and the difference between
  never signed and invalidated belongs to history. One attestation per
  field means a second signer replaces the first and is told whose hand now
  stands, rather than being refused: a role-form would otherwise be locked
  to whoever asked first. Revocation is not offered, because `REVOKE` above
  is a decision's verb and an attestation is erased by content, not withdrawn.
- A signature also keeps whatever reference the host gives for the ceremony
  that produced it, as opaque text: the engine stores that a hand attested
  and with what citation, never that it vouches for the citation.
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
changes — and an authority somebody has to be able to consult: RUNTIME
§5.11 reads the entries back to a caller, and its fold rebuilds a state from
them and compares it with the snapshot at every revision, which is what makes
"the latest state is an optimization" a checked claim rather than a property of
a format nobody has replayed. A working state is private, uncommitted data.

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
requirement to expose stateful receiver methods. `History` is the one line
whose shipped shape says more than `([]Commit, error)` can: the answer carries
where to continue from, the head as the call read it, and a count of the
entries the actor's permission did not reach (RUNTIME §5.11), because a page of
history that silently omitted what a caller may not see would be an audit with
holes punched in it instead of an audit with a fence.

### 6.1 View

`View(paths...)` accepts **multiple paths**, returns each selected
subtree (including descendants), merges overlapping selections, and uses
one consistent working snapshot. With no paths, it returns the entire
permission-filtered case view. The shipped result carries the case's
committed revision, its stage, the revision of the draft the values came
from, and the merged roots (RUNTIME §10).

A unified view contains both structural/form metadata and dynamic data:
effective permissions, values, presence, requiredness,
verification/signature state, validation errors, dynamic options and
children. View rendering must enforce `view` permissions at every node.
Clients need no copy of the DSL.

Enforcement is *pruning*, not flagging: content the actor may not see,
and content the effective state does not include (a closed `present`
guard), is absent from the answer rather than present and marked. The
rule is about what a view is allowed to *do* while answering, not only
about what it returns: a node built and then filtered would still have
had its dynamic options fetched and its validation hook run, which are
two ways to disclose a field the mask withholds — the first as work the
caller's own rule performed on withheld data, the second as a message
naming it. A selection that prunes away entirely returns no nodes, which
is not an error; only a path the actor has no permission for refuses.

Values, conditions and requiredness are read from the actor's draft;
roles and stage from the committed case, so a draft cannot grant the
actor anything (invariant 6). The two revision numbers a view reports are
therefore the case's and the copy's, and reading a case never creates a
working copy for the reader. Presence is stated per node — an empty
collection is distinguishable from an empty optional group, and an unset
cell from a cell holding `""` — because a client that cannot tell those
apart cannot offer the right action.

Verification state is printed per node: whether a decision may be
addressed *here*, and what the governing unit's decision is. Both halves
carry a subtlety worth stating at the spec. The offer is false at a row of
a `verifiable` collection and at a nested `verifiable` declaration, because
§4.2 reviews a collection as a whole and lets the outermost annotation
govern — an address that cannot be decided must not advertise that it can
(RUNTIME §5.9). And the decision is read from the committed case while the
value beside it is read from this actor's draft, so a view can honestly
report an approval of content the caller is looking away from; the two
statements are about different content, which is invariant 6's split
(RUNTIME §5.7).

Signature state is read the same way and from the same half of the split as
the decision beside it: a node says whether a hand may attest there and, when
one has, whose — the grant folded for this actor and the attestation the case
holds, both of them, while the value next to them is this actor's own draft.
What the view does not answer is the field's other half, who the declaration
names, because that is a fact about a person and a read is not asked to guess
which person is looking; `Sign` is where it is settled, and a host that wants
the answer asks the operation (RUNTIME §5.10).

Dynamic `Options` hooks are called by the engine when a requested view
needs the options—not through a separate client `GetOptions` API. One
binding answers for a field rather than for each of its instances, so a
collection costs one call, and a declared `options` field with no
registered provider is an execution failure for the whole view: an empty
choice list would be a silent version of the same bug.

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
  reject with comment, or revoke their own current active decision. The
  shipping shape adds three things the bullet leaves open. The target is
  the *governing declaration*: a path that resolves to a nested
  `verifiable` annotation or through a row of a collection is refused by
  name of the unit that does answer (§4.2's two rules, enforced rather
  than warned). One decision is active per unit, so a second approval
  replaces the first and says whose judgement now stands. And a decision is
  made about the revision the reviewer was *shown* — the same guard §6.4
  states for all three operations — while the number stored beside it is the
  one its own commit produced. §4.2's reset is then the whole of staleness,
  as the caller experiences it: an approval is not marked stale, it is gone,
  and the reason it is gone is in history and not in the state.
- `Sign`: signed form must be fully submitted/valid with approved
  applicable verification units; enforce the signature field's
  authorized user/role, bind to form content and the observed global
  revision. The shipping shape reads that sentence as two questions and one
  fact. The questions are a grant and an identity: `allow sign` at the
  field's own slot, and the field's declared holder — that role's holders, or
  the user the committed cell names. The identity is asked before the form's
  readiness, so an actor who lost the assignment hears that he is not the
  signer rather than that the form is not ready; a definition may grant the
  first without granting the second, and the language does not require them to
  agree. The fact is that "bind to form content" is stated as a number and not
  a digest: what stands beside the name is the revision the signature's own
  commit produced, and §4.2's invalidation is what makes that honest, which
  keeps the second question of a later gate an array read. A form that is not
  ready is refused with the list of what it still wants rather than with a
  permission message, and no hook is asked: the content a signature binds has
  already passed the gates at its own commit. A signature is not revocable and
  not mergeable — one attestation per field per content, so a second signer
  replaces the first and says whose hand now stands (RUNTIME §5.10).
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

The shipped shape of both verbs — what the merge takes, what it refuses to
decide, and what a conflict may say — is RUNTIME §5.8.

## 7. CaseContext and hooks

`CaseContext` is a **read-only, stable snapshot view** used by hooks. It
provides access to case identity, actor, stage, values, reviews,
signatures and effective roles without giving hooks mutation or storage
privileges. It is a logical interface, not necessarily a copy of the
state. What it answers is bounded by the same permission as the screen:
a value the calling actor may not view is not available to a host rule
either, because a hook's read discloses exactly as a rendered node does,
and a wider channel wearing a host's function is the one hole this
design's containment argument cannot close by itself.

Keep hook contracts separate:

```text
Validate(goCtx, case, path, value) -> Valid(normalizedValue)
                                    | Invalid(errors)
                                    | Failure(error)
Options (goCtx, case, path)        -> options | Failure(error)
OnCommit (goCtx, case, commit)     -> observer result
```

`case` is the read-only context above and `goCtx` the request's
cancellation and deadline: a hook that does I/O needs the first, a host
rule that consults the case needs the second, and naming them apart is
what stops "context" from meaning two things in one signature. Two of
`Validate`'s three sketched results ship — an error refuses, a returned
value is stored — and the third, *invalid and keep it*, is a decision
about what an accepted `Update` reports rather than a signature detail,
so RUNTIME §9 leaves it open instead of inventing it.

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

The shipped shape (RUNTIME §6) is that paragraph in Go: a void function
on a runtime-registered list, dispatched at the tail of the operation
that committed, after the store's lock releases, handed the record the
trail renders at the committing actor's mask. The sketch's
`observer result` dropped by subtraction: the only fact the engine could
act on is the commit's outcome, and acting on it is exactly what this
section forbids, so there is nothing left for a return type to carry. An
observer that panics unwinds to the caller with the commit durable and
nothing recovers it — deciding what a broken host function means is
delivery policy, which is the host's half and stays the host's.

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

The host implements [`monshi.Storage`](../storage.go). `Create` and `Load`
store and retrieve the committed head; `Draft` reads a working copy without
creating one. `InitDraft` atomically returns an existing copy or inserts
revision zero if the head still matches its proposed base. `Save` updates
an existing draft with an optimistic revision check, and `Discard` removes
it with a revision guard. `Commit` compares the head, appends an immutable
entry, replaces the committed state and advances eligible draft lineage in
one transaction. `History` returns a dense page in ascending revision order.
The engine prepares changes; the host chooses the transaction technology.

A separate binary abstraction can expose `Put(io.Reader) -> BinaryRef`
and `Get(BinaryRef) -> io.ReadCloser` with appropriate request contexts.

Milestone 2 ships a store *behind* this shape rather than this interface
itself: an in-memory seam, unexported, whose methods are named for the
same acts and whose two revision checks are exactly the ones this section
requires.
`NewRuntime` wires it in, because a host has no store to hand over yet.
Milestone 4 supplied the implementation and the seam became exported
contract; what did not change then is the operations' shapes, which is
the point of keeping the seam narrow now.

The API may be refined during implementation, but it must not weaken
these atomicity and revision guarantees.

Backends report `ErrNoSuchCase`, `ErrNoDraft`, or `*Stale` carrying the
current revision. Other host errors arrive under `STORAGE_FAILURE` with
the original cause. `MemStorage` is the reference byte-storage implementation.

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

Milestone 2 has taken five items out of the order above, and the reason
belongs here because the list is the contract's. `Submit` ships with the
commit path it cannot be separated from — §6.3's two revision guards,
completeness and validation against the candidate, the forward diff, and
the history entry and snapshot swap that must be consistent with each
other. None of it is reachable through `Update` alone, and a milestone that
ended at a working copy would leave invariants 1, 2, 5 and 6 unexercised.
Milestone 5's commit work is whole now: the seam §4.2 asked for runs both of
its erasures on every commit, the `verif` half arriving with `Review` and the
`sigEntry` half with `Sign`, which is why the pair is one list the store owns
rather than two rules each operation has to remember. So is its last noun:
historical replay landed with the read that asked for it (RUNTIME §5.11), which
is where the two verdict entries §5.6 justified per operation stop being
provisions and start being answers. RUNTIME §10 states what exists today.

`Transition` follows it out of order for the same reason and one of its own: it
is the other operation that commits, and unlike `Review` and `Sign` it needs
nothing that does not exist yet — a declared edge, two masks, one condition and
the completeness pass `Submit` already runs. Of the destination's `require` it
runs all three conjuncts today: completeness, the `verif` units, and the
signatures a `required` field must hold, the last arriving with `Sign` (§4.2,
RUNTIME §5.5). Two details of §6.4's sketch are refined by the shipping shape:
the result is a `TransitionResult` rather than a `CommitResult`, because the
`affected` path set promised above is a stage move's own answer — paths whose
*visibility* moved, with no value behind them — while a submit's affected set
is the fanout over the paths its commit recorded, which the same edit's batch
had usually named already (RUNTIME §5.4, §9); and the number the call takes is
named for the thing it is, the observed committed revision, since invariant 5's
refusal is about what the caller was shown rather than what it holds. Invariant
5's second half is now enforced by code rather than by intent.

`View` is the third item taken out of order, and its reason is the one
the other two lacked: it is not an operation that can be deferred past
the two that already shipped. `Update` answers with an affected set and
`Submit` with missing and invalid lists, and both are actionable only by
a client that can render the paths they name — so without a read there
is no way to observe half of what milestone 2 built, and no place for
invariant 7 or for §7's hook contract to be tested at all. Milestone 3's
*views* half is therefore paid here, and its hook half was owed beside it:
a view that fetched an `Options` list through an unexported registry had
been testing the engine's own wiring, and exporting a signature means
handing the host the read-only context to pass into it as well, which is
one contract and not two changes — which is why RUNTIME §6 exports both at
once and not a signature a slice ahead of its `CaseContext`. Milestone 3's
last item, affected-path calculation, arrived with the fanout (RUNTIME
§5.3).

Two of §6.1's lines are refined by the shipping shape: enforcement is
pruning rather than flagging, and presence is reported in the data
rather than as a second flag. A third is refined by what a decision *is*:
verification state is offered only at the address that can be decided, so
the flag and the operation agree instead of one inviting what the other
refuses. A fourth is refined by the same rule read the other way: signature
state arrives as a pair of fields, where a hand may attest and which hand did,
and the declaration's other half — the holder its entry names — is not a
read's answer, because a view is not asked which person is looking at it
(RUNTIME §5.10).

`Review` is the fifth item taken out of order, and it is the first taken
from milestone 5 rather than from milestone 2's neighborhood. The reason is
the same one every other item in this list has had: everything it needs
already exists, and everything the *next* item needs exists only after it.
A decision asks the mask, the path seam and the revision guard that
§6.3 and §6.4 already enforce, and it is the operation §4.2's reset needs
beside it: a commit that clears a unit whose content it just changed is not
testable while nothing can stand approved to be cleared, and neither is
SYNTAX §7's walkthrough, which reaches `company_review` only after somebody
approves. `Sign` is the milestone's last item, and it lands as the two halves
the earlier ones left standing: signature validity as a third conjunct of a
destination's `require`, and invalidation of a signature bound to content that
moved, both paid through the commit seam `Review` had opened. Its own state is
one table — a name, the revision that bound it, and the host's reference —
dense by slot beside the decisions it joins in history, and no operation but
this one writes a signature field. `Rebase` and `Discard` read the commit
history, the committed snapshot
and the per-user copies invariant 4 forces the store to keep, so they add no
state of their own, and invariant 5's "stale working states cannot Submit" was
a rule with no operation that could make a state fresh. Milestone 6's observer
half has landed from the engine's side: `OnCommit` is a runtime seam
(RUNTIME §6), dispatched at the tail of each operation that commits and
handed the trail's own record, masked at the actor whose act it was.
What remains the host's is the half this section always assigned it —
delivery, retries, the fan-out to whoever else should hear. The
dependency this deferral once claimed is gone from both directions: a
notification renders the commit's own diff, and every result that
commits now answers with an affected-path list of its own (RUNTIME §5.4,
§5.9, §5.10, §9), so the observer and the client's refresh no longer
wait on each other. The other two hooks
of §7 are shipped, signature and read-only context together. The deferral at the
end of §6.5 survives its own implementation — the merge takes a side nobody
moved and refuses to choose between two sides that did, so the detailed
automatic merge rules and conflict presentation this section leaves out are
still left out, and the policy that would decide them is a rule no document
here has written.

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
