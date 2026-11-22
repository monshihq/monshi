# Monshi — Runtime Environment Design Specification

**Status:** Design baseline for the runtime (DESIGN §10 milestones 2–5)
**Companions:** [DESIGN.md](DESIGN.md) — authoritative for engine
behavior; [SYNTAX.md](SYNTAX.md) — authoritative for the language
**Language:** Go
**Scope:** Lowering compiled definitions into a fast, secure runtime
image; interpretation semantics; hook execution; cost model

Citations: `DESIGN §x` and `SYNTAX §x` name the companion documents;
bare `§x` names a section of this document. Bare rule IDs (`N2`, `F3`,
`S2`, `P1`, `ST3`, `E2`, …) are SYNTAX §5 static-semantics rules.

---

## 1. Scope and pipeline

The `schema` package (SYNTAX §5.8) turns definition source into a
compiled definition: resolved names, types, paths and conditions in a
pointer-tree shape optimized for checking, not execution. The runtime
described here consumes that output.

The root package `monshi` is the resting place of the runtime:

```text
source ──schema.Parse──▶ *schema.Schema
       ──lower (monshi, unexported)──▶ image
       ──interpret──▶ View / Update / Submit / Review / Sign
                      / Transition
```

`monshi.Load(id, reader)` (DESIGN §3) runs the front end, rejects the
definition if `Schema.Errors` is non-empty, and lowers the remainder
into an image. Neither `*schema.Schema` nor the image is exported:
hosts pass source and receive `Case` handles; the engine never
interprets trees of interface types at request time.

Authority: DESIGN.md governs what operations mean; this document
governs how the definition is stored and executed. A conflict between
this document and DESIGN.md is a bug in this document.

---

## 2. Why the schema shape is not an execution shape

### 2.1 The interpretive workload

Every runtime task from DESIGN §4–§6 reduces to four interpretive
jobs, driven entirely by the definition, never by case-authored code:

1. **Condition evaluation** — `present[...]`, `required[...]` and edge
   guards: boolean trees of comparisons over scalar leaves, literals
   and enum tags (SYNTAX §3.6). No loops, no calls, no arithmetic
   (E5).
2. **Permission resolution** — per (actor role, node): default-deny
   union of allows, deny-override across the scope chain
   Case → Form → Group → Field (DESIGN §4.3).
3. **Effective-state recomputation** — presence pruning, required
   satisfaction, verification resets and signature invalidation on
   committed content change (DESIGN §4.1–§4.2).
4. **Stage-graph checks** — edge lookup, guard evaluation, destination
   `require` invariants (DESIGN §4.4).

### 2.2 Cost profile of the compiled model

`schema`'s exported types are diagnostic-shaped, and their fields show
it. `Node.Kind`, `Expr.Op` and `Type.Base` are `string`s; `Node.Path`
is a dotted absolute path; children are `[]*Node` of individually
allocated nodes; `Grant.Perms` is `[]string`. The path-keyed maps that
resolve references (`analyzer.nodes`, `formNodes`, `stages`) are
internal to the checker — they exist to *report* problems, and they do
not survive into the output.

Running the four jobs against that shape means, per request:

- dispatching on string tags (`Kind == "field"`, `Op == "&&"`,
  `Base == "decimal"`) where the answer is one of ~10 constants;
- walking `Expr` recursively through individually allocated structs —
  the worst locality profile a `View` traversal can have;
- comparing and prefix-testing `Path` strings to match conditions to
  their operands (SYNTAX §5.3-F7's self-reference test is literally
  `strings.HasPrefix`);
- scanning `Grant.Perms` with string equality, per permission, per
  grant, per node, to answer "may this role edit this field";
- re-deciding, on every request, facts the checker already proved once.

None of this is wrong; it is the shape of *checking*. The runtime
image's purpose is to make each such fact a constant.

### 2.3 The two alternatives, and why they were rejected

**Compile to an embedded scripting language** (Lua, Starlark, JS):
the only option that injects a Turing-complete runtime into the
engine. The DSL deliberately has no loops and no function calls
(E5; DESIGN §4.4 — "No functions are called inside DSL expressions");
a scripting VM reintroduces both and needs instruction budgets and
host-binding audits to restore containment. Enum tags, `decimal`
semantics and form-anchored relative paths would need a foreign
binding layer larger than the lowering itself, and interpreting
`kind == COMPANY` as a script call is slower than a native closure.
Rejected on security, speed and DESIGN §1 minimal-dependency grounds.

**Design a new VM from scratch**: buys a serializable IR, a stable
binary format and (eventually) JIT. Costs an instruction-set spec, a
dispatch loop (slower than direct closures for trees of ~10 node
kinds), a bytecode verifier and tooling. None of what a VM buys is a
current requirement. Rejected; §9 records the fork where it could
become one.

Adopted: **lowering into domain-specific structures — slot tables for
static facts, compiled closures for dynamic ones.** The precedent is
`regexp` and `text/template`: compile once, execute against a private,
dense representation that no caller can see.

---

## 3. The runtime image — data model

Illustrative Go, all unexported, root package. Names are provisional;
the shapes are the contract.

### 3.1 Identity and interning

```go
type nodeID   uint32 // dense slot index: every form, group, field
type stageID  uint8  // declared stage ordinal; 0 = none
type roleID   uint8  // declared role ordinal; ordinals start at 1
type typeID   uint16 // built-in or custom type
type tagID    uint16 // enum tag, dense within its owning type
type valueID  uint32 // index into the image's constant slotValue table
type condID   uint32 // index into the condition table; 0 = none
type hookID   uint16 // host hook binding; 0 = none
type stringID uint32 // interned name: high bits offset, low bits len
type roleMask uint64 // set of roleID bits, for stage transition rights
```

Names are interned at lowering. Afterwards, no runtime comparison, map
lookup or allocation touches a definition name string: paths are slot
indexes, tags are ordinals, kinds are `uint8`. This is sound because
N3/N4 resolved every reference at compile time, and because case *data*
values never compare against definition names except enum tags, which
become `tagID`s (T3).

The two `uint64` sets — `roleMask` (§3.7) and `requires` — fix a
documented ceiling of 64 declared roles and 64 top-level forms. That is
a ceiling on definitions, not on cases: `Load` rejects a definition over
it with an internal error, and lifting the limit later means widening
those two fields to word slices, which changes no semantics and no
algorithm in this document.

### 3.2 Node slots

```go
type nodeSlot struct {
	kind     uint8    // form | group | field
	name     stringID // for rendering and paths, not for logic
	path     stringID // absolute path, for View/affected reporting
	parent   nodeID
	first    nodeID   // children occupy the dense range
	n        uint32   // [first, first+n), declaration order
	typ      typeID   // 0 on role-typed fields; see role
	role     roleID   // role-typed field: the role it confers (T4)
	flags    uint16   // Required | Multiple | Verifiable
	condPres condID   // present[cond]; 0 = unconditional
	condReq  condID   // required[cond]; 0 = plain / none
	def      valueID  // default in img.consts (F5); 0 = none
	verif    nodeID   // governing verifiable unit; 0 = none
}
```

The image holds `[]nodeSlot`, built by one pass over `Schema.Forms`.
Dense child ranges turn `View` iteration into array walks over
contiguous memory. `verif` settles F3 precedence at lowering: each node
stores the *outermost* `verifiable` unit that governs it, because the
parent's verification takes precedence — no ancestor scan runs at
request time.

### 3.3 Types and values

```go
type typeInfo struct {
	base uint8                    // primitive tag (T1: exactly one)
	tags []tagID                  // enum: dense, own type only (E3)
	fits func(decimal.Decimal) bool // decimal(p, s), prebuilt (T2)
	hook hookID                   // Validate binding, by custom type (§6)
}
```

`Type.Base` strings and `Value.Kind` strings disappear into these
ordinals. T2 guarantees precision/scale legality, so `fits` is built
once per decimal type. F5 guarantees defaults are constants of the right
shape, so each default becomes a materialized `slotValue` in
`img.consts`, which the runtime copies into a frame and never parses —
including ISO 8601 dates, which the compiler already turned into ordered
`time.Time` values.

### 3.4 Conditions as combinators

```go
type condFn func(*frame) bool

// The complete constructor set the lowering may emit:
func eqStr(n nodeID, want stringID) condFn
func eqTag(n nodeID, want tagID) condFn
func eqBool(n nodeID, want bool) condFn
func cmpOrder(n nodeID, op cmpOp, v valueID) condFn // < <= > >=
func andAll(cs ...condFn) condFn // n-ary, flattened at lowering
func orAll(cs ...condFn) condFn
func neg(c condFn) condFn
func always() condFn             // folded constant (ST6)
func never() condFn
```

A `frame` exposes values by slot: fixed fields read `f.value(n)`, and
F6 (uniform conditions inside `multiple`) means a collection condition
evaluates once for the collection, not per instance. E2 guarantees
every operand is a single scalar leaf, so each leaf getter is compiled
against its known type — **there is no type dispatch inside a running
condition**. E4 and ST6 guarantee the whole tree is boolean, so `condFn`
needs no error or validity channel; there is no partial-evaluation
state to thread.

Constant folding: a `CONSTANT_CONDITION`-flagged guard (ST6) lowers to
`always()` or `never()`. A `never()` guard makes its edge
deterministically unsatisfiable — identical semantics, zero evaluation.

### 3.5 Permissions as masks

The five targeted permissions become bits — `view=1`, `edit=2`,
`submit=4`, `review=8`, `sign=16`. `accept`/`reject` get no bits: P1
confines them to stage blocks, where they are represented as role sets
(§3.7), not as per-node masks.

```go
type permTables struct {
	allow []uint8 // per slot: bits held on that slot or an ancestor
	deny  []uint8 // per slot: bits denied on that slot or an ancestor
	over  []stageOverlay // sparse: stages that alter this role's bits
}

type stageOverlay struct {
	at    stageID
	slots []nodeID // slots this stage's targeted grants touch
	add   []uint8
	del   []uint8
}
```

Two facts about placement. Role-block grants are unconditional: they
fold into the role's `allow`/`deny` arrays, propagated down the scope
chain once at lowering (DESIGN §4.3's Case → Form → Group → Field
inheritance becomes table construction, not request-time walking).
Stage-block targeted grants are **stage-scoped**: `allow Registrar
view, edit, submit archive;` inside `company_review` holds while the
case is in `company_review` and nowhere else, so it lowers to that
stage's overlay on the Registrar's tables rather than into the base
arrays. `over` is kept sorted; lookup is a scan of a one- or two-entry
list in practice.

Resolution at request time is then pure bitwise algebra:

```text
effective(roles R, stage s, slot t) =
    ( OR r in R: table(r).allow[t] | stageAdd(r, s, t) )
      &^ ( OR r in R: table(r).deny[t] | stageDel(r, s, t) )
```

Deny-override "across all effective roles and scope inheritance" is
the `&^` line: any denied bit in the actor's full role set strips the
bit, whoever granted it, and a deny from one role strips a permission
held through another. Default-deny is the absence of bits.

P2/P3 remove the remaining runtime checks: every grant target is
already a slot, `submit` targets are top-level forms, `review` targets
are `verifiable` declarations, `sign` targets are signature fields and
`edit` never targets a signature field. No runtime target-resolution or
compatibility test against a mask bit is ever needed.

### 3.6 Signature entries

`Node.Signer` and `Node.SignField` (S2's resolution output) lower to
exactly one non-zero field per signature slot:

```go
type sigEntry struct {
	role     roleID // signature(Role): authorized = that role's holders
	assigned nodeID // signature(field): authorized = committed value
}
```

S2 resolved every `sig_ref` to one of the two forms and casing (N2)
guarantees they cannot collide, so "exactly one is non-zero" is an
image invariant: no re-resolution, no fallback path, no name test. S3's
"first eligible holder" and "committed value of that role-typed field"
rules are read directly off these two fields.

### 3.7 Stage tables

```go
type edge struct {
	dest   stageID
	guard  condID // 0 = unconditional edge
	reject bool   // reject edge, skips destination require (§5.5)
}

type stageInfo struct {
	requires uint64 // bitset over top-level form slots (ST4)
	accept   roleMask // roles permitted to accept OUT of this stage
	reject   roleMask // roles permitted to reject out of it
	edges    []edge   // dest unique (ST3): direct scan, ≤ a few
}
```

Transition authority lives here and nowhere else — P1 guarantees no
role-block statement may mention `accept`/`reject`, so a transition bit
has no representation in the per-node masks of §3.5. ST1 makes
`image.initial` a constant ordinal. ST5's reachable set may be stored
for diagnostics, but the warning itself is compile-time only: an
unreachable stage still lowers and still behaves.

### 3.8 The assembled image

```go
type image struct {
	slots   []nodeSlot   // dense; forms are the roots (§3.2)
	types   []typeInfo   // §3.3
	consts  []slotValue  // defaults and guard literals (§3.3)
	conds   []condFn     // condition table (§3.4)
	perms   []permTables // one per declared roleID (§3.5)
	sigs    []sigEntry   // one per signature slot (§3.6)
	stages  []stageInfo  // §3.7
	initial stageID      // first declared stage (ST1)
	depFrom []depRange   // per slot: adjacency bounds into depOwners
	depOwn  []nodeID     // condition-bearing owners reading that slot
}

type depRange struct{ first, n uint32 }
```

One allocation of `slots`, one of each table, and no map inside the
image: the whole definition becomes index-addressable arrays.
`depFrom`/`depOwn` are §5.3's fanout structure, built once from F7's
acyclic graph.

---

## 4. Lowering rules — what each guarantee becomes

Every SYNTAX §5 rule that constrains a definition lets the runtime
delete a check. This table is the anti-drift contract between the two
documents: if lowering ever needs something in the third column, a
front-end guarantee was broken.

| Rule | Lowering bakes in | Runtime never does |
| --- | --- | --- |
| N2/N3/N4 | ordinals and interned names | resolve or disambiguate a name |
| T1–T3 | built `typeInfo`, dense tags | parse a type expression |
| T2, F5 | prebuilt `fits`, materialized defaults | validate a default |
| F2 | flags exist only where legal | test attribute legality |
| F3 | `verif` = outermost unit | scan ancestors for verification |
| F4 | collection-level review unit | split reviews per member |
| F6 | one closure per collection condition | per-instance condition variants |
| F7 | dependency adjacency ranges | detect cycles on write |
| S1 | signature entries at form level | search groups for signers |
| S2/S3 | `sigEntry`: role XOR assigned field | re-resolve a `sig_ref` |
| P1 | transition role sets on stages | mix targeted and transition bits |
| P2/P3 | grants as slot masks | resolve or type-check a target |
| P4 | stage grants as `roleID`s | look up a grant's role by name |
| P6 | deny folded into masks | re-derive precedence per request |
| ST2/ST4 | `dest` ordinals, `requires` bitset | look up a stage or form name |
| ST3 | one `edge` per destination | choose between parallel edges |
| ST6 | `always`/`never` folds | evaluate a provably fixed guard |
| E1 | uppercase operands are `tagID`s | classify name-shaped values |
| E2 | one typed leaf getter per operand | type-test or walk into aggregates |
| E3/E4 | fixed comparison shape, bool tree | re-check operand pairing |
| E5 | closed constructor set (§3.4) | run unrepresentable operations |

Warnings have image consequences too. `REDUNDANT_VERIFIABLE` is the
compiler narrating `verif` folding (F3): the redundant annotation simply
does not appear in the image. `EMPTY_FORM` (F1) lowers to a legal slot
with `n == 0`, which §5.2 and §5.4 then treat as trivially satisfied.
`EMPTY_GROUP` (F1) and `EMPTY_BLOCK` (ST1) are errors, so no empty group
and no empty `forms` or `stages` block exists to lower at all.
`UNUSED_ROLE` and `UNUSED_TYPE` (P5) declarations still lower —
harmless, referenced by no mask and no getter. `UNREACHABLE_STAGE` (ST5)
leaves no trace: such a stage lowers and behaves, it is just not
reachable by construction. `CONTRADICTORY_GRANTS` (P5) is likewise
resolved, not represented: the §3.5 fold applies deny-override, so the
contradiction has exactly one deterministic outcome per bit.

---

## 5. Interpretation semantics

### 5.1 Frames and data layout

```go
type slotValue struct {
	set bool
	i   int64            // integer, boolean, enum tag
	d   decimal.Decimal  // decimal(p, s)
	s   string           // string data
	t   time.Time        // date, datetime
	bin BinaryRef        // opaque content handle (DESIGN §5.2)
}

type frame struct {
	img    *image
	commit []slotValue // committed snapshot, dense by slot
	work   []slotValue // candidate overlay during Update
	insts  []instance  // multiple collections, keyed by stable ID
}

type instanceID uint64 // stable identity, host-visible (DESIGN §5.1)

// One row per instance of one multiple collection.
type instance struct {
	coll  nodeID            // the collection slot that owns this row
	row   []slotValue       // dense over descendant slots
	index map[instanceID]int // stable ID → row position
}
```

A row carries every payload field but reads exactly one of them: the
class is fixed per slot by `typeInfo`, so a getter selects the field at
lowering time — there is no runtime tag test, and no `any` boxing on the
hot path. The `frame` allocates two dense rows per slot once, not per
node per request.

A `multiple` group stores one row per instance whose positions are the
collection's descendant slots; instances are addressed by stable ID as
an ordinary path segment, matching DESIGN §5.1 — `shareholders.42`,
with no `[id:42]` syntax. Condition closures capture only slot indexes,
so swapping a frame (committed → working candidate)
redirects every getter for free — that swap is what makes Update's
candidate evaluation and Submit's final evaluation share one code path.

Instance rows are the one place where a key taken from case data indexes
anything: `instance.index`, a per-collection `map[instanceID]int`
bounded by that collection's committed size, mapping stable IDs to row
positions rather than to nodes. Definition names never appear in it,
which is the scope of §7's claim about name-keyed execution.

### 5.2 Presence and requiredness

```text
present(n, f) = n.condPres == 0 || cond(n.condPres)(f)

// Consulted only when present(n, f):
satisfied(n, f) =
    n.flags.Required → nonEmpty(n, f) || n.def != 0
    n.condReq == 0   → true                        // not required
    otherwise        → !cond(n.condReq)(f) || nonEmpty(n, f)
```

The last two branches are exclusive. `required` and `required[expr]`
are mutually exclusive variants of one attribute (SYNTAX §4), kept
apart by `DUPLICATE_ATTRIBUTE`, so a slot carries at most one of them:
`flags.Required` set together with `condReq != 0` is a malformed image,
reported as an internal error at lowering (§10).

Presence controls structural presence in the *effective* state, so a
node whose `present` is false is not part of what Submit evaluates —
its requiredness is simply not reached. That is a different thing from
a cleared value: DESIGN §5.1 keeps `Empty(path)` distinct from
presence-driven absence precisely because an emptied-but-present field
still fails `required`. `nonEmpty` therefore tests the committed value,
not the node's existence.

A false `present` removes the affected current data at commit without
rewriting history (DESIGN §4.1): the effective view computes `present`
top-down and treats a false node as structurally absent, so pruning is
derived, never stored. `required multiple` means ≥1 committed instance
exactly when the condition holds — F6 uniformity makes that one
evaluation for the whole collection (SYNTAX §4).

### 5.3 Update fanout and the affected set

DESIGN §7 requires dependencies and conditions to be re-evaluated
after `Validate` normalization. F7's acyclic dependency graph lowers to
`img.depFrom`/`depOwn`: the condition-bearing owners whose truth can
change when a slot changes, as adjacency ranges over a flat array. For
an `Update` write set W, the re-evaluated set is
`W ∪ transitive-closure(depFrom, W)`, computed with a pre-sized queue
whose bound is fixed at lowering.

The `affected` paths reported back (DESIGN §6.2) are a superset: the
closure above, plus the subtrees whose *permission* or presence
changed, plus the fields whose `Options` binding was reached —
`affected` covers options, permissions, presence and validation, not
only value changes. No path strings are built to compute it, and no
map is probed: results are slot sets rendered on demand. This is the
machinery behind DESIGN §6.2's "changes such as province and city are
evaluated together".

### 5.4 Submit completeness

`Submit(form, workingRevision)` (DESIGN §6.3) checks the form's working
revision and `base_revision == current committed revision`, then walks
the form's dense subtree: `present ∧ satisfied` over §5.2, with
`Validate` clean at this candidate. Defaults count as satisfaction
without implying prior commitment (DESIGN §4.1). Verification and
signature requirements are evaluated through `verif` units and
`sigEntry`, all index-based. An empty diff means no commit is written
(DESIGN §6.3: no meaningless commits).

### 5.5 Transition checking

```text
Transition(actor A, stage s → dest d, observed revision r):
  roles = A's primary role ∪ committed role-typed assignments (T4)
  e     = stage[s].edge[d]            // absent → undeclared transition
  mask  = e.reject ? stage[s].reject : stage[s].accept
  permit = roles ∩ mask ≠ ∅                        // P1, §3.7
  guard  = e.guard == 0 || cond(e.guard)(committed frame)
  if !e.reject:
      for f in stage[d].requires:     // ST4 bitset
          satisfied throughout f ∧ verif units APPROVED ∧ sigs valid
  require r == case revision          // DESIGN §6.4
```

`reject` skips destination completeness but not permission, edge or
guard (DESIGN §4.4). ST3's one-edge-per-destination rule is what makes
"the caller selects the intended destination" a direct `(s, dest)`
lookup instead of a search with ambiguity. Permission for the *targets*
touched by the transition (e.g. writing a stage-scoped field after
arrival) resolves through §3.5 with `s` = the current stage.

### 5.6 Commit effects precomputed

Verification reset and signature invalidation are triggered by
committed content change (DESIGN §4.2), and are part of the commit
(DESIGN §6.3). Because `verif` and the owning `sigEntry` are stored per
slot, the commit diff maps to reset and invalidate targets by direct
indexing: the whole question "what becomes stale when this field
changes" is answered at lowering, in O(1) per changed slot.

---

## 6. Hook execution contract

DESIGN §7 fixes hook semantics; the image decides *where* they
intercept. Hooks are host code: the containment argument in §7 covers
definition content, and hooks sit outside it, bound by the read-only
`CaseContext` contract instead.

- **`Validate` is bound by custom type** (DESIGN §7 lookup rule).
  `typeInfo.hook` carries it; the engine calls it for every value
  assigned to a field of that type and stores the *normalized* value
  for all later checks — the §5.3 re-evaluation, Submit's final
  validation, and the commit diff alike.
- **`Options` is bound by field path.** Lowering attaches the binding to
  `options`-typed slots; the view layer fetches when a requested view
  needs options (DESIGN §6.1), so `view` permission gates option sets
  exactly as it gates values. There is no separate client `GetOptions`
  call, and E2 is what keeps `options` values out of conditions
  entirely.
- **Re-evaluation order:** apply edits → normalize per field
  (`Validate`) → re-evaluate the §5.3 closure → report validation
  errors against final values. Submit re-runs the whole chain against
  current rules (DESIGN §7).
- **Failures:** missing required hooks, hook execution failures,
  structural errors and forbidden edits reject the whole `Update`
  atomically; ordinary validation errors persist with draft values for
  UI feedback (DESIGN §6.2; the error taxonomy is DESIGN §9).
- **`OnCommit`** receives the actual durable `Commit` and the
  post-commit frame (DESIGN §7). It is observer-only: it never vetoes or
  rolls back the commit that triggered it, and a delivery failure cannot
  invalidate a durable commit (DESIGN §7 and DESIGN §9). Hooks never
  rerun during replay of old commits (DESIGN §5.2) and are not called
  inside long-held storage transactions (DESIGN §7 and DESIGN §8). It
  reads the image, never modifies it.

---

## 7. Security properties

1. **Closed combinator set.** The only executable definition content is
   `condFn` trees the lowering can construct (§3.4). A definition cannot
   express a loop, a call, an allocation pattern or an arithmetic
   operation, because the grammar cannot express it and the emitter only
   emits the fixed constructors. Containment by construction, not
   sandboxing: there is no interpreter to escape from.
2. **No name-keyed execution.** Interned slots and ordinals replace
   every definition-name decision (§3.1), which removes unbounded
   attacker-chosen path strings from the request path entirely. Keys
   derived from case data are limited to stable instance IDs (§5.1).
3. **Bounded work.** Condition trees have a fixed depth and node count
   at lowering; dependency fanout is closed under a compile-computed
   transitive set with a fixed bound. Worst-case work per Update or
   Transition is a function of definition size, known before any data
   arrives.
4. **Immutable image, read-only sharing.** The image is written once,
   during lowering, and never mutated: any number of goroutines and
   actors evaluate against it without locks. Mutable case state lives in
   frames and storage only.
5. **Fail-closed at the seam.** `Load` refuses any `Schema.Errors`, and
   warnings never reach the runtime as behavioral input (SYNTAX §5):
   the image is built only from definitions that already satisfy every
   invariant §4 relies on.
6. **Data never parses.** Defaults and guard literals were format-checked
   at compile time (§3.3); the runtime assigns typed values or rejects
   through hook errors. The image has no string-parsing code path
   reachable from case data — the only ISO 8601 parsing in the system
   is the compiler's, on literals inside definitions.

---

## 8. Cost model and benchmarks

Targets are guidance to be measured, not SLOs. The reference corpus is
the SYNTAX §7 example plus a generated wide case: one form, 40 fields,
6 roles, 5 stages, 1,000 `multiple` instances.

| Benchmark | Measures | Expectation vs tree baseline |
| --- | --- | --- |
| `BenchmarkCondEval` | `.kind == COMPANY` guard | ~10–30× faster, 0 allocs |
| `BenchmarkPermResolve` | view bit for 1k nodes | 2 bitmap ops per node |
| `BenchmarkViewWide` | permission-filtered view | array walks, no path build |
| `BenchmarkUpdateFanout` | affected set | closure over adjacency ranges |
| `BenchmarkTransitionGuard` | edge and requires | index-based throughout |
| `BenchmarkLowering` | schema → image, corpus | linear, well under 1 ms |

The lowering benchmark doubles as the serialization argument (§9):
rebuild is cheap enough that persisting images is not worth its
complexity today.

---

## 9. Non-goals and evolution paths

- **No image serialization.** `Load` rebuilds images from source, which
  is affordable per §8 and consistent with DESIGN §3's explicit-load
  rule. If a future service must ship compiled definitions across
  process boundaries without re-checking, the evolution is a *data-only*
  encoding of the slot tables plus guarded operand descriptors — a
  closed, decodable structure, still not a general VM. That is the
  option-3 fork of §2.3, and it must be justified by measurement first.
- **No JIT, no bytecode ISA, no embedded scripting engine** (§2.3).
- **No hot reload of images.** Replacement is an explicit `Load`
  (DESIGN §3); definition-compatibility rules are unchanged by this
  document.
- **No automatic `Rebase` merging** (DESIGN §6.5 deferral stands).
- **No per-instance condition paths** (SYNTAX Appendix B item 11): F6
  uniformity is load-bearing for §3.4's one-closure-per-collection. If
  instance-relative conditions are ever added, `condFn` gains an
  instance parameter and the collection getters re-generalize — that is
  the one place where this image shape must change.
- **The image is not a public API.** Tooling that wants definition
  structure consumes `schema`, which is human-readable and
  diagnostic-shaped, not `monshi` internals.

---

## 10. Handoff from the front end

Milestone status (DESIGN §10): 1 is complete — the `schema` package,
its tests and the two language documents. This document is the
specification basis of milestone 2 (*pure case semantics: in-memory
state, stable paths, permissions, conditions, stages*) and defines the
substrate milestones 3–5 execute on.

Contract at the seam:

- `schema` remains the only consumer of source text and exports nothing
  beyond its diagnostics and resolved model (SYNTAX §5.8).
- Lowering reads only exported `Schema` fields and treats the §4
  invariants as assumptions. A violated assumption is an internal
  defect: reported through the `SCHEMA_MISMATCH` category with the
  offending slot (DESIGN §9), never laundered into data corruption, and
  the front end is not re-run to recover.
- Conformance gate: the SYNTAX §7 example must (a) compile with zero
  diagnostics, (b) lower to the shape traced in Appendix A, and
  (c) execute Appendix A's runtime steps as a test once milestone 2
  lands.

---

## Appendix A — Worked trace of the SYNTAX §7 example

Lowering output, qualitative but concrete:

- `legal_form` → `typeInfo{base: enum, tags: [PERSON=1, COMPANY=2]}`;
  field `application.kind` gets an `eqTag`-shaped getter over
  `slotKind`. `fee : amount` → `typeInfo{base: decimal,
  fits: fitsDecimal(14,2)}`.
- Draft's `accept[application.kind == COMPANY] company_review` →
  `edge{dest: company_review, guard: eqTag(slotKind, COMPANY)}`. The
  `PERSON` twin targets `person_review`, so both survive: ST3 bounds
  multiplicity per *destination*, not per source stage. The two
  conditional accepts are two entries, never one ambiguous choice.
- `draft`'s `allow Manager accept, reject;` →
  `stage[draft]{accept: {Manager}, reject: {Manager}}` as `roleMask`
  bits. Nothing is written to `permTables`: P1 gave transitions no
  per-node representation.
- `deny edit application.internal` (Applicant) folds to
  `deny[slotInternal] |= edit` and propagates into
  `deny[slotRiskScore]` through the ancestor fold — DESIGN §4.3's scope
  rule applied once, in the compiler. Manager's
  `allow edit application.internal` folds only into `allow`. Effective
  `edit` for a pure Manager on `application.internal.risk_score` is
  `edit &^ ∅` = granted; for a pure Applicant it is `edit &^ edit` =
  denied; for an actor holding both roles it is denied, which is
  exactly "deny overrides allow across all effective roles".
- Relative condition `internal present[.kind == COMPANY]` anchors at
  the form during lowering (SYNTAX §3.6) → `eqTag(slotKind, COMPANY)`.
  The stage-block absolute path `application.kind` resolves to the same
  slot: the relative/absolute distinction is invisible to the runtime.
- `shareholders required[.kind == COMPANY] multiple` → one `condReq`
  closure (F6), evaluated per collection in Submit (§5.4);
  `verif[holderName] = verif[shareholders] = slotApplication`, because
  the form is the outermost `verifiable` unit. A nested `verifiable`
  group would have drawn `REDUNDANT_VERIFIABLE` and folded to the same
  value (F3).
- `submitted : signature(Applicant)` → `sigEntry{role: Applicant}`;
  `approved_by : signature(.reviewer)` → `sigEntry{assigned:
  slotReviewer}`. Signing checks read those two fields only: the
  role-holder set for the first, the committed value of
  `application.reviewer` for the second (T4, P6).
- `allow Registrar view, edit, submit archive;` inside
  `company_review` → a `stageOverlay{at: company_review}` on the
  Registrar's tables listing `archive`'s subtree. It is absent from the
  base arrays, so the same actor sees no `edit` on `archive` while the
  case sits in `draft`.

Runtime steps for `Transition(reviewer-user → "draft" →
"company_review")`, where that user is the committed value of
`application.reviewer` and therefore holds `Manager` (T4):

1. Role set = primary role ∪ committed `Manager` via `slotReviewer`.
2. `edge[draft][company_review]` found by direct scan; kind is accept.
3. `roles ∩ stage[draft].accept` = {Manager} ∩ {Manager} ≠ ∅ — passes.
   An actor who is only an Applicant fails here, and so does one who is
   only a Registrar: `draft` grants accept to nobody else.
4. Guard `eqTag(slotKind, COMPANY)` on the committed frame reads one
   dense slot. True admits this destination; the `PERSON` twin is not
   consulted, because the caller named `company_review` (ST3).
5. `stage[company_review].requires` = the `application` bit: §5.4
   completeness under the effective frame, `verif` units APPROVED —
   `application` is verifiable as a whole, so the unit is the form —
   `submitted` valid for the current content version, and `approved_by`
   valid iff the committed `reviewer` user signed that content.
6. Revision check against the global committed revision (DESIGN §6.4),
   then one atomic forward commit: the stage change, plus whatever
   §5.6 maps to. Here that is nothing — no verifiable content changed.
7. `OnCommit` fires after the commit is durable with the actual
   `Commit` and post-commit frame (§6), never inside the transaction.

The trace touches: three integer sets (role set, accept mask, requires
bitset), one `condFn`, and subtree walks bounded by definition size.
Zero string comparisons, zero map lookups, zero type dispatch.
