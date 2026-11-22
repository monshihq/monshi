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

`monshi.Load(id, reader)` (DESIGN §3) runs the front end, refuses the
definition if `Schema.Errors` is non-empty, and lowers the remainder into
an image. `*schema.Schema` and the image both stay unexported: a host
passes source and receives an opaque `*Definition`, from which `Create`
will mint `Case` handles as the operations land. The engine never
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
type tagID    uint16 // enum tag, dense in the global uppercase namespace
type valueID  uint32 // index into the image's constant slotValue table
type condID   uint32 // index into the condition table; 0 = none
type hookID   uint16 // host hook binding; 0 = none
type stringID uint32 // interned definition name: index into image.names
type roleMask uint64 // set of roleID bits, for stage transition rights
```

Names are interned at lowering. Afterwards no *interpretive* step
compares, hashes or allocates a definition name: paths are slot indexes,
tags are ordinals, kinds are `uint8`, permissions are bits. This is
sound because N3/N4 resolved every reference at compile time, and
because case *data* values never compare against definition names except
enum tags, which become `tagID`s (T3).

Host-supplied names are the one exception, and they arrive only at the
seam: §3.9 resolves a path or stage name to an ordinal before
interpretation begins, so no name survives into a request's inner work.

The two `uint64` sets — `roleMask` (§3.7) and `requires` — fix a
documented ceiling of 64 declared roles and 64 top-level forms. That is
a ceiling on definitions, not on cases: `Load` rejects a definition over
it with an internal error, and lifting the limit later means widening
those two fields to word slices, which changes no semantics and no
algorithm in this document.

### 3.2 Node slots

```go
type nodeSlot struct {
	kind     uint8    // root | form | group | field
	name     stringID // for rendering and paths, not for logic
	path     stringID // absolute path, for View and affected reporting
	parent   nodeID
	first    uint32   // children occupy [first, first+n) in image.children
	n        uint32   // direct children, declaration order
	span     uint32   // preorder subtree size, so [id, id+span) is the subtree
	typ      typeID   // 0 on role-typed fields; see role
	role     roleID   // role-typed field: the role it confers (T4)
	flags    uint16   // Required | Multiple | Verifiable
	condPres condID   // present[cond]; 0 = unconditional
	condReq  condID   // required[cond]; 0 = plain or not required
	def      valueID  // default in image.consts (F5); 0 = none
	hook     hookID   // Options binding, by path (§6); 0 = none
	verif    nodeID   // governing verifiable unit (F3); 0 = none
	sig      uint32   // index into image.sigs; 0 = not a signature field
}
```

The image holds `[]nodeSlot` plus `[]nodeID` of children, built by one
preorder pass over `Schema.Forms`. Two dense ranges describe the tree:
`span` makes a subtree a contiguous index interval, and `[first, first+n)`
lists direct children in declaration order, so `View` iteration is an
array walk over contiguous memory. `parent` exists for the scope fold
only: it is read at lowering and by nothing at request time. `verif`
settles F3 precedence at lowering: each node stores the *outermost*
`verifiable` unit that governs it, because the parent's verification
takes precedence — no ancestor scan runs at request time.

`kindRoot` occupies slot 0 and is the target of `all` in a grant, which
is why one bit pattern reaches every declaration: inheritance from the
root is the fold, not a special case. A slot carries no name→slot map:
`path` renders, and the case's data rows are addressed by slot ordinal
(§5.1).

### 3.3 Types and values

```go
type typeInfo struct {
	base uint8                     // primitive class (T1: exactly one)
	tags []tagID                   // enum only, declaration order (T3)
	fits func(decimal.Decimal) bool // decimal(p, s), prebuilt (T2)
	hook hookID                    // Validate binding, by custom type (§6)
	name stringID                  // declared name, for problem text only
}
```

`Type.Base` strings and `Value.Kind` strings disappear into these
ordinals. T2 guarantees precision/scale legality, so `fits` is built
once per decimal type — a closure, not a bounds compare the runtime has
to assemble from precision and scale.

Tags are numbered in the definition-wide uppercase namespace N3 defines
(role names share it, so `Applicant` and `PERSON` can never both be
declared), and `image.tagType[tag]` records which enum owns each
ordinal. That back-reference is what lets an operand pair be settled
once: `kind == COMPANY` is legal only if `COMPANY`'s owning type is the
field's type. The front end already refuses a mismatch as E3's
`TYPE_MISMATCH`; lowering checks it again because §4's contract runs in
both directions — a guarantee it was handed is an assumption it may
compile against, and a comparison that could only ever answer false is
drift, not data.

SYNTAX §5.8 makes every constant in the compiled model already
normalized to the type it belongs to: defaults are materialized
`slotValue`s in `img.consts`, and comparison literals carry the
operand's type — `date`/`datetime` guards arrive as ordered
`time.Time`, integer literals against `decimal` operands arrive as
decimals. Lowering *asserts* that shape instead of repairing it: a
literal whose kind disagrees with its operand's `typeInfo` is the §10
internal error, not something the runtime papers over. Nothing here is
parsed at request time.

### 3.4 Conditions as combinators

```go
type condFn func(stateView) bool // stateView is §5.1's frame row, by slot

// The complete constructor set lowering may emit:
func isTrue(n nodeID) condFn                 // the boolean operand E4 allows
func notTrue(n nodeID) condFn                // !.flag, built without a wrapper
func strCmp(n nodeID, op cmpOp, want string) condFn
func intCmp(n nodeID, op cmpOp, want int64) condFn
func decCmp(n nodeID, op cmpOp, want decimal.Decimal) condFn
func timeCmp(n nodeID, op cmpOp, want time.Time) condFn
func tagCmp(n nodeID, op cmpOp, want tagID) condFn
func boolCmp(n nodeID, op cmpOp, want bool) condFn
func strSlotCmp(a, b nodeID, op cmpOp) condFn // and int/dec/time/tag/bool
func andAll(cs ...condFn) condFn              // n-ary, flattened at lowering
func orAll(cs ...condFn) condFn
func allSet(slots []nodeID) condFn            // E6's absence guard
func neg(c condFn) condFn
func always() condFn                          // folded constant (ST6)
func never() condFn
```

Every constructor except the three combinators and `allSet` closes over
exactly one or two slot indexes plus one pre-typed constant: the
comparison and the operator are both fixed at lowering, so evaluation is
one row read, one presence test, one typed compare. There is **no type
dispatch inside a running condition** — E2 guarantees a scalar leaf
operand, T1 fixes its class, and `typeInfo` decided which constructor to
emit. `allSet` is the only constructor over a *list* of slots, and it is
emitted only under a `!`, for E6's absence rule.

A `frame` exposes values by slot, so swapping the committed frame for a
candidate redirects every getter (§5.1). F6 makes a collection condition
evaluate once for the collection, never per instance. E4, E6 and ST6
guarantee the tree is boolean, so `condFn` needs no error or validity
channel: nothing inside a condition can fail, and there is no
partial-evaluation state to thread.

Constant folding: `cond` asks `readsData` before it emits anything, so a
guard with no path operand becomes `always()` or `never()` and costs no
evaluation. A `never()` guard makes its edge deterministically
unsatisfiable — identical semantics, zero work. ST6's
`CONSTANT_CONDITION` warning is reported for transition guards only, and
the fold does not depend on it: `present[!(1 < 2)]` is legal, unflagged
source whose group is absent in every state, and lowering still emits
`never()`.

Folding works per subtree, not only per guard, because `cond` tests the
same `readsData` question before it emits anything: a `&&` arm of `1 < 2`
becomes `always()` and costs one call inside a conjunction that reads
data anyway. A guard whose every arm folded this way lowers to one
closure over no slot: it owns no fanout (§5.3) and answers without data,
and §10's verify pass probes exactly that by evaluating it twice, once
against an empty case and once against one where every slot is filled.

The reverse inclusion holds by construction rather than by check. The
only way a closure obtains a `nodeID` is `leafSlot`, and `leafSlot`
registers the read on the scope building the closure, so a condition
cannot touch a slot it did not declare. That is what lets §5.3's fanout
be sound with nothing tracking reads at request time.

Two number literals are one comparison class inside the evaluator,
whatever their widths: `1 < 2.5` folds to `always()` on the decimal
scale, which is §3.3's normalization rule applied to a constant pair
rather than to a slot and its operand.

`condID 0` means unconditional: `image.conds[0]` exists, is never called,
and is `never()` so that a caller which forgot the convention fails
closed. Every `condID` a slot or edge actually names has a built
closure, which verify also asserts.

### 3.5 Permissions as masks

The five targeted permissions become bits — `view=1`, `edit=2`,
`submit=4`, `review=8`, `sign=16`. `accept`/`reject` get no bits: P1
confines them to stage blocks, where they are represented as role sets
(§3.7), not as per-node masks.

```go
type permTables struct{ byRole []permTable } // indexed by roleID; index 0
type permTable struct {                      // is no role, grants nothing
	allow []uint8        // per slot: bits held on it or an ancestor
	deny  []uint8        // per slot: bits denied on it or an ancestor
	over  []stageOverlay // stage-scoped targeted grants, sorted by stage
}

type stageOverlay struct {
	at    stageID
	slots []nodeID // every slot the stage's grants reach, sorted, unique
	add   []uint8  // parallel to slots
	del   []uint8  // parallel to slots
}
```

Two facts about placement. Role-block grants are unconditional: they
fold into the role's `allow`/`deny` arrays, propagated down the scope
chain once at lowering (DESIGN §4.3's Case → Form → Group → Field
inheritance becomes table construction, not request-time walking).
Stage-block targeted grants are **stage-scoped**: `allow Registrar
view, edit, submit archive;` inside `company_review` holds while the
case is in `company_review` and nowhere else, so it lowers to that
stage's overlay on the Registrar's table rather than into the base
arrays. A container grant in either layer reaches its subtree, so the
overlay row lists every slot the grant touches, merged and deduplicated;
`over` is sorted by stage and probed by binary search, and a role with no
stage-targeted grant has no rows at all.

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

The two layers are not ordered against each other, which is what makes
`CONTRADICTORY_GRANTS` (P5) representable rather than a defect: a stage
that allows and denies the same permission to the same role on the same
slot sets both bits, and one AND-NOT resolves them. Lowering keeps the
contradiction instead of resolving it into the arrays, because the
front-end warning is about the *source*, and a request-time answer must
still be one algebra (§4's P6 row).

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
	name     stringID // rendering and problem text only
	requires uint64   // bitset over formID ordinals (ST4)
	accept   roleMask // roles granted accept out of this stage
	acceptNo roleMask // roles denied accept out of it
	reject   roleMask // roles granted reject out of it
	rejectNo roleMask // roles denied reject out of it
	edges    []edge   // dest unique (ST3): direct scan, ≤ a few
}

func (s *stageInfo) mayAccept(roles []roleID) bool // held∩accept − acceptNo
func (s *stageInfo) mayReject(roles []roleID) bool
```

Transition authority lives here and nowhere else — P1 guarantees no
role-block statement may mention `accept`/`reject`, so a transition bit
has no representation in the per-node masks of §3.5. ST1 makes
`image.initial` a constant ordinal. ST5's reachable set may be stored
for diagnostics, but the warning itself is compile-time only: an
unreachable stage still lowers and still behaves.

The two `…No` masks exist because §3.5's deny-override rule does not
stop at the node masks: `deny Helper accept;` inside a stage block must
strip accept from an actor who also holds a role that was granted it,
which is invisible until the roles are known per request. Clearing the
grant bit instead would make the denial absolute and lose the grant for
everyone else, so authority is `(held ∩ accept) ≠ ∅ ∧ (held ∩ acceptNo)
= ∅` over both masks — the same shape as the node algebra, one word
wider. Ordinal 0 confers nothing, so an actor with no roles transitions
nowhere.

### 3.8 The assembled image

```go
type image struct {
	id       string
	slots    []nodeSlot  // dense, preorder; slot 0 is the case root (§3.2)
	children []nodeID    // child lists, one dense range per slot (§3.2)
	names    []string    // interned definition names, never case data (§3.1)
	types    []typeInfo  // §3.3
	consts   []slotValue // defaults and guard literals (§3.3)
	conds    []condFn    // condition table; index 0 is never called (§3.4)
	perms    permTables  // one folded table per declared roleID (§3.5)
	sigs     []sigEntry  // indexed by nodeSlot.sig (§3.6)
	stages   []stageInfo // §3.7
	forms    []nodeID    // top-level forms, indexed by formID (ST4)
	hooks    []hookKey   // binding keys the engine looks host code up by (§6)
	tagNames []stringID  // indexed by tagID
	tagType  []typeID    // indexed by tagID: the enum that owns each tag
	roleIdx  []stringID  // indexed by roleID
	depFrom  []depRange  // per slot: adjacency bounds into depOwn (§5.3)
	depOwn   []nodeID    // condition-bearing owners that read that slot
	depBound uint32      // closure queue bound: no slot is visited twice
	initial  stageID     // first declared stage (ST1)
}

type depRange struct{ first, n uint32 }
```

One allocation of `slots`, one of each table, and no map inside the
image: the whole definition becomes index-addressable arrays. Every
index-0 row is a placeholder for "none" — no role, no stage, no type, no
tag, no signature, no condition — which is why ordinals and array
indexes agree everywhere without a subtraction at any hotspot.
`depFrom`/`depOwn` are §5.3's fanout structure, built once from F7's
acyclic graph, with `depBound` fixing the queue a request needs before
it runs. `children` is separate from `slots` so a slot row stays one
cache line's worth of hot fields; `subtree(n)` is the dense preorder
range `[n, n+span)` and `childrenOf(n)` the slice, so §5.4's walks are
index loops with no pointer chasing.

`names` is interned definition text and is read by nothing on a request
path: `View`, `affected` and problem text render from it. It is the only
place definition text is stored at all — `slotValue`'s own `s` field
holds case data and guard literals, never names. Case data names never
enter `names` (DESIGN §5.1's stable IDs are host values).

### 3.9 The resolution seam

DESIGN §6 lets a host address a case in the language's own vocabulary:
`View(paths…)`, `Submit(form)`, `Sign(path)`, `Review(path)`,
`Transition(target)`, and an actor's primary role by name. So a
definition name does reach the runtime — exactly once, as a key, before
any interpretation. The claim §3.1 makes is about what happens *after*
that: resolution turns the caller's text into ordinals, and nothing
downstream reads a string.

```text
resolve path "application.internal.risk_score":
    cur = 0                                  // the case root
    for seg in segments(path, '.'):          // byte slices of the
        if child of cur named seg exists:    //   caller's own string,
            cur = that child                 //   never a copy
        else if cur is a collection and seg parses as a stable ID:
            row = frame.instance(cur, id)    // §5.1's one data-keyed map
        else:
            return unknown path              // DESIGN §9, not a fallback
    return cur
```

No name-keyed map is needed, because the tree the image already holds is
the index: a declaration's siblings are contiguous in `children`, member
names are unique within a container (N3), forms and stages are few, and
a comparison is a length check plus a byte compare against interned
text. Work is bounded by the caller's own string length and by
declaration breadth — both known before any request arrives — with no
allocation: `path[i:j]` on a Go string is a header, not a copy. Role,
tag, form and stage names resolve the same way against their closed
arrays (`roleIdx`, `tagNames`, `forms`, `stages`).

Two properties follow, and they are the whole of §7.2's containment
argument. First, a name can select *data*, never *behavior*: after
resolution the request walks slots, bits and closures, and no code path
depends on which letters the host typed. Second, the only key taken from
case data is a stable instance ID inside `instance.index`, bounded by
that collection's committed rows (§5.1) — there is still no
data-keyed-anything in the definition.

An unmatched or mistyped path is DESIGN §9's unknown-path error at the
seam. It is never resolved to a near miss, and there is no partial
answer to interpret: `nodeID`, `stageID` and `roleID` ordinals exist only
for declared things.

Resolution arrives with the operations that need it (§10); the image's
shape is what makes it cheap, and is already fixed.

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
| E6 | `allSet` guard under every `!` | read absence as satisfaction |

Warnings have image consequences too. `REDUNDANT_VERIFIABLE` is the
compiler narrating `verif` folding (F3): the redundant annotation simply
does not appear in the image. `EMPTY_FORM` (F1) lowers to a legal slot
with `n == 0`, which §5.2 and §5.4 then treat as trivially satisfied.
`EMPTY_GROUP` (F1) and `EMPTY_BLOCK` (ST1) are errors, so no empty group
and no empty `forms` or `stages` block exists to lower at all.
`UNUSED_ROLE` and `UNUSED_TYPE` (P5) declarations still lower —
harmless, referenced by no mask and no getter. `UNREACHABLE_STAGE` (ST5)
leaves no trace: such a stage lowers and behaves, it is just not
reachable by construction. `CONSTANT_CONDITION` (ST6) becomes the
`always`/`never` fold of §3.4. `CONTRADICTORY_GRANTS` (P5) is the one
warning the image does carry: both bits stay set, because which of them
the actor's roles reach is a per-request fact, and §3.5's AND-NOT gives
the contradiction exactly one deterministic outcome per bit. Statement
order therefore cannot matter — allow-then-deny and deny-then-allow
lower to the same two bits.

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
	commit stateView // committed snapshot, dense by slot (§3.4)
	work   stateView // candidate overlay during Update
	insts  []instance // multiple collections, keyed by stable ID
}

type instanceID uint64 // stable identity, host-visible (DESIGN §5.1)

// One row per instance of one multiple collection.
type instance struct {
	coll  nodeID            // the collection slot that owns this row
	row   stateView         // dense over descendant slots
	index map[instanceID]int // stable ID → row position
}
```

A `stateView` is the shipped parameter type of `condFn` (§3.4): one
`slotValue` per slot, in slot order. Interpretation passes a frame's
`commit` or `work` row and nothing else, which is why a condition needs
no frame pointer and no notion of which state it is being asked about.

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

Absence is what makes those two lines mean something, and E6 fixes it:
a comparison over a slot with no value is unsatisfied, `!=` included, so
`present[.kind == COMPANY]` is false in a fresh case rather than
accidentally true. Negation inherits the same answer instead of
inverting it — `!` over data lowers to `allSet(reads) && !inner` (§3.4),
which is why `required[!(.n > 3)]` does not demand a value from a case
where `n` was never entered. Absence is therefore never a hidden third
truth value inside a closure: `condFn` returns `bool`, and the third
state lives in `slotValue.set`, tested by the comparator that reads it.

Frames materialize defaults before evaluation (§5.1), so "unset" means
genuinely without a value: a defaulted field is never absent, and
`Empty(path)` on a present field is an empty value, not absence —
which is exactly why an emptied `required` field still fails (§5.4).

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
after `Validate` normalization. F7's acyclic condition graph lowers to
`img.depFrom`/`depOwn`: for each slot, the declarations whose *own*
guard reads it, as sorted adjacency ranges over one flat array. For an
`Update` write set W the walk is a breadth-first queue over those
ranges, and the re-evaluated set is `W ∪ transitive-closure(depFrom, W)`.

Two properties make that walk cheap and bounded. The adjacency is
slot-indexed, so one write reaches its dependents by array bounds rather
than by searching a graph: no map, no allocation, no pointer chasing.
And a slot is enqueued at most once, which fixes the queue's bound at
lowering — `img.depBound`, the slot count — before any request knows
what it is about to write. F7's acyclicity is what makes the closure
finite at all; the bound is what makes it non-surprising.

Owners are the declarations a guard hangs off (§5.2), not the guards
themselves: a group with `present[…]` is one re-evaluated unit, and its
subtree is what §5.2 and §5.4 then walk. So `depOwn` answers "which
declarations must I ask again", and the declaration's own condition
closure answers the question.

Edge guards are deliberately absent from the adjacency: they own no
path. Nothing in a slot's affected set changes when a transition
guard's operand is edited, because that guard is asked at the moment of
a `Transition` and nowhere else (§5.5). Including it would fan an
ordinary edit out to stages the case may never reach.

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
  e     = stage[s].edge[d]            // none → undeclared transition
  grant = e.reject ? stage[s].reject : stage[s].accept
  denial = e.reject ? stage[s].rejectNo : stage[s].acceptNo
  permit = held(roles) ∧ grant ≠ ∅ ∧ held(roles) ∧ denial == ∅  // §3.7
  guard  = e.guard == 0 || cond(e.guard)(committed frame)
  if !e.reject:
      for f in bits(stage[d].requires):   // ST4 bitset
          satisfied throughout f ∧ verif units APPROVED ∧ sigs valid
  require r == case revision          // DESIGN §6.4
```

`permit` is two mask tests on one word, not a rule walk: authority is
granted by one role and not stripped by a denial held through any other,
which is P6's deny-override applied to transitions (§3.7). The guard is
then evaluated against the committed frame — an edge guard was lowered
like any other condition (§3.4), so this is one closure call over dense
slots, and it is the only reason `Transition` reads case data at all.
Note the asymmetry with §5.3: edge guards are *not* in the fanout
adjacency, because nothing about a path's affected set changes when a
guard's operand is edited — the guard is asked at the moment of the
transition and nowhere else.

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

What the image records is a *binding*, never host code: `image.hooks` is
indexed by `hookID` and each entry is one `hookKey` of the two kinds
§3.8 defines — by custom type name or by field path. An engine maps a
key to a function in its own registry. A definition therefore cannot
name a hook, choose one by data, or reach one it did not declare: the
only hooks in the image are the ones `Validate` and `Options` attributes
produced, one per distinct binding.

---

## 7. Security properties

1. **Closed combinator set.** The only executable definition content is
   `condFn` trees the lowering can construct (§3.4). A definition cannot
   express a loop, a call, an allocation pattern or an arithmetic
   operation, because the grammar cannot express it and the emitter only
   emits the fixed constructors. Containment by construction, not
   sandboxing: there is no interpreter to escape from.
2. **No name-keyed execution.** Definition text reaches the runtime
   only as a lookup key at the seam, once per request, and the answer is
   an ordinal (§3.9). Interned slots and ordinals then replace every
   name decision §3.1 lists, so no code path depends on letters an
   attacker typed, and no unbounded name space is ever searched: the key
   sets are the closed ones the definition declared. Work is bounded by
   the caller's own string and by declaration breadth. Keys derived from
   case data are limited to stable instance IDs (§5.1).
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
   a violated §4 invariant refuses too: either way the host receives no
   definition, so nothing half-built can ever answer a request (§10).
   Warnings never reach the runtime as behavioral input (SYNTAX §5):
   they are handed to the host and change nothing in the image, and the
   image is built only from definitions that already satisfy every
   invariant §4 relies on.
6. **Data never parses.** Defaults and guard literals were format-checked
   at compile time (§3.3); the runtime assigns typed values or rejects
   through hook errors. The image has no string-parsing code path
   reachable from case data — the only ISO 8601 parsing in the system
   is the compiler's, on literals inside definitions.

---

## 8. Cost model and benchmarks

The rows below are measured, with `go test -bench . -benchtime=1s`, on
an Apple M4 (Go 1.27.4, darwin/arm64). `benchmark_test.go` is the
harness; a number here without a benchmark in that file is a target, not
a result. The reference corpus is the SYNTAX §7 example (20 slots) plus
§8's generated wide case: one form, 40 fields, one 8-field `multiple`
group, 6 roles, 5 stages — 51 slots.

| Benchmark | Measures | Result |
| --- | --- | --- |
| `BenchmarkCondEval` | one `tagCmp` guard | 2.1 ns, 0 allocs |
| `BenchmarkPermResolve` | 51 slots, one role | 407 ns, 0 allocs |
| `BenchmarkLoweringOnly` | schema → image, §7 example | 12.5 µs, 247 allocs |
| `BenchmarkLoweringWideOnly` | schema → image, wide | 29.8 µs, 294 allocs |
| `BenchmarkLowerSpecExample` | source → image, §7 | 41.7 µs, 1073 allocs |
| `BenchmarkLoweringWide` | source → image, wide | 59.4 µs, 1213 allocs |

Three things follow. A condition is two memory reads and a compare, so
§3.4's "no type dispatch inside a running condition" is a cost claim and
not just a design claim. A permission answer is one array index, an
overlay binary search and an AND-NOT, and it allocates nothing: 8 ns per
node is below the cost of deciding what to show for it. And a whole
definition lowers in tens of microseconds, with the front end accounting
for 3.3× the bill — which is §9's argument for rebuilding images rather
than serializing them, stated in numbers.

What is *not* measured yet, because the substrate to run it is not
shipped: `BenchmarkViewWide`, `BenchmarkUpdateFanout` and
`BenchmarkTransitionGuard` need the frame of §5.1 and arrive with the
operations. Nor is any "× faster than the compiled tree" ratio: that
would mean an interpreted-tree harness, and building one to prove a
point the absolute numbers already make is a cost of its own. The
multiples this section used to promise are dropped; the measured rows
above are the facts, and a regression is now something a re-run shows.

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
its tests and the two language documents. Milestone 2 (*pure case
semantics*) is in progress: the image of §3, the lowering of §4, and the
`Load` seam are in place, and what remains is the frame of §5.1 plus the
operations that read it. This document defines the substrate milestones
3–5 execute on.

Contract at the seam. The exported surface is deliberately tiny:

```go
func Load(id string, src io.Reader) (*Definition, error)
func (d *Definition) ID() string
func (d *Definition) Warnings() []Problem

type Problem struct { Line, Col int; Code Code; Context, Msg string }
type Code string

type Error struct{ ID string; Problems []Problem; Defects []string }
func (e *Error) Mismatch() bool
```

One more name is exported: `BinaryRef`, a `[]byte` with no behavior.
DESIGN §5.2 puts content handles in case *data*, and §5.1's value row
reserves the field now so a row never changes shape when the binary
store arrives; nothing in this package reads or resolves one.

- `schema` remains the only consumer of source text and exports nothing
  beyond its diagnostics and resolved model (SYNTAX §5.8). `Load` calls it
  once and never re-runs it, whether or not lowering succeeds.
- Lowering reads only exported `Schema` fields and treats the §4
  invariants as assumptions. A violated assumption is an internal
  defect: reported through the `SCHEMA_MISMATCH` category with the
  offending slot (DESIGN §9), never laundered into data corruption.
- One error type, two causes, and the cause is not something a host has
  to parse out of a message. `Problems` is what the author must fix, with
  the codes and positions the front end chose; `Defects` is what the
  runtime must fix, and `Mismatch` says which. Either way `Load` returns
  no definition: a refused definition has no image at all (§7.5).
- `Definition` is immutable and safe for concurrent use, because nothing
  in it is written after `Load` returns (§7.4). Warnings are reported and
  kept (§4): they never invalidate a definition, and they are the only
  thing about a loaded definition an author would still want to read.
- Duplicate definition IDs (DESIGN §3) are a registry rule, and the
  registry arrives with `Create`. Two `Load`s of one source yield two
  equivalent, independent definitions; naming them the same is the host's
  mistake to reject.
- Conformance gate: the SYNTAX §7 example must (a) compile with zero
  diagnostics, (b) lower to the shape traced in Appendix A, and
  (c) execute Appendix A's runtime steps as a test once milestone 2
  lands. (a) and (b) are `TestSpecExample*` in `lower_test.go`, with the
  source read out of SYNTAX.md so the fixture cannot drift from the
  document it is traced against; (a) is also checked through `Load`, so
  the seam is not merely documentation.

---

## Appendix A — Worked trace of the SYNTAX §7 example

Lowering output, qualitative but concrete:

- `legal_form` → `typeInfo{base: enum, tags: [PERSON=1, COMPANY=2]}`;
  field `application.kind` gets a `tagCmp` closure over its slot.
  `fee : amount` → `typeInfo{base: decimal, fits: decimalFits(14,2)}`,
  and its default becomes one `image.consts` row (§3.3).
- Draft's `accept[application.kind == COMPANY] company_review` →
  `edge{dest: company_review, guard: tagCmp(slotKind, cmpEq, COMPANY)}`.
  The `PERSON` twin targets `person_review`, so both survive: ST3 bounds
  multiplicity per *destination*, not per source stage. The two
  conditional accepts are two entries, never one ambiguous choice. The
  guard owns no fanout (§5.3): it is `condOwner` 0.
- `draft`'s `allow Manager accept, reject;` →
  `stage[draft]{accept: {Manager}, reject: {Manager}, acceptNo: ∅,
  rejectNo: ∅}` as `roleMask` bits (P6 needs the denial words even when
  a definition leaves them empty). Nothing is written to `permTables`:
  P1 gave transitions no per-node representation.
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
  the form during lowering (SYNTAX §3.6) →
  `tagCmp(slotKind, cmpEq, COMPANY)`.
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
3. `stage[draft].mayAccept(roles)` — one word test: Manager is in
   `accept` and in no `…No` mask, so it passes. An actor who is only an
   Applicant fails here, and so does one who is only a Registrar:
   `draft` grants accept to nobody else.
4. Guard `tagCmp(slotKind, cmpEq, COMPANY)` on the committed frame is one
   presence test and one ordinal compare. True admits this destination;
   the `PERSON` twin is not consulted, because the caller named
   `company_review` (ST3).
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
§3.9 turned the caller's `"company_review"` into a `stageID` before any
of it ran, so the interpretation itself does no name comparison and no
type dispatch, and probes no map — a case's only data-keyed lookup is a
collection's instance index (§5.1), and a transition that names no
instance never reaches it.
