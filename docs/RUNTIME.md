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
rules are read directly off these two fields — one membership test against
the role fold, or one read of the committed cell — and §5.10 gives the
reason they cannot be folded into the mask: a grant and an identity are
declared in different statements, and the language does not require them
to agree.

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
            steps += (cur, id)               // §5.1's one data-keyed map
            if reach(steps) fails: return unknown path
        else:
            return unknown path              // DESIGN §9, not a fallback
    return cur, reach(steps)                 // a slot and its cell
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

The instance path an answer carries is a fixed array, not a list: the
number of steps is at most the number of `multiple` declarations that
enclose one node, which lowering refuses past a documented ceiling of
four. A definition that loaded therefore cannot produce a path that
overflows the answer, and a resolution allocates nothing — measured as
`BenchmarkResolvePath`, §8.

Two properties follow, and they are the whole of §7.2's containment
argument. First, a name can select *data*, never *behavior*: after
resolution the request walks slots, bits and closures, and no code path
depends on which letters the host typed. Second, the only key taken from
case data is a stable instance ID inside `instances.index`, bounded by
that collection's committed rows (§5.1) — there is still no
data-keyed-anything in the definition.

An unmatched or mistyped path is DESIGN §9's unknown-path error at the
seam. It is never resolved to a near miss, and there is no partial
answer to interpret: `nodeID`, `stageID` and `roleID` ordinals exist only
for declared things.

Resolution is shipped as `resolve.go`; the operations that use it come
next (§10). The image's shape is what makes it cheap, and was fixed
before the seam was written.

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
| S1 | signature entries, and each form's span | search groups for signers |
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

S1's second column hides a second baking-in now that `Sign` exists: the pass
that resolves a signer also stamps the dense span of the form's signature
slots (§3.2's `form`, §5.6's range), so the question §5.5's third conjunct
asks — which signatures does this form have — is answered out of the image,
and nothing walks a form's children looking for one.

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
	s   string           // string data, and options values
	t   time.Time        // date, datetime
	bin BinaryRef        // opaque content handle (DESIGN §5.2)
}

type cell struct {
	slot nodeID   // the declaration
	row  stateView // which row holds this one value
	idx  int      // where in that row
}

type instanceID uint64 // stable identity, host-visible (DESIGN §5.1)

type instStep struct { coll nodeID; id instanceID }

// One instance: its stable ID, its values, and any collection inside it.
type instRow struct {
	id   instanceID
	row  stateView // dense over the slots this instance holds
	nest []*instances // collections declared inside this one
}

// One collection's rows, in creation order, plus the ID → position map.
type instances struct {
	coll  nodeID
	rows  []instRow
	index map[instanceID]int
}

// One snapshot of a case's data: the base row and every collection's rows.
type state struct {
	img   *image
	row   stateView
	insts []*instances // indexed by nodeSlot.coll-1, the dense ordinal
	seq   instanceID   // next stable ID, part of the case's data
}

type frame struct {
	img    *image
	commit state // what is committed
	work   state // what the actor is about to commit
}
```

A `stateView` is the shipped parameter type of `condFn` (§3.4): one
`slotValue` per slot, in slot order. Interpretation passes a state's own
row and nothing else, which is why a condition needs no frame pointer
and no notion of which state it is being asked about, and why the two
states in a frame are the same shape rather than two representations.

A `cell` is what a read, a write and a condition all agree on: the slot,
the row, the position. Addressing a member costs one subtraction —
`n - rowLo(coll)` — because numbering is preorder (§3.2), and the same
fact is why a member's offset makes sense only against the *innermost*
collection enclosing it. `rowLo` is that collection's first row slot:
its first descendant for a repeated group, itself for a repeated field,
since `multiple` is legal on both (SYNTAX §4) and a field's instances
are values *of* that field. A collection's own slot, in short, belongs
to its parent's row and never to its own.

A row carries every payload field but reads exactly one of them: the
class is fixed per slot by `typeInfo`, so a getter selects the field at
lowering time — there is no runtime tag test, and no `any` boxing on the
hot path. A frame allocates its two dense rows once, not per node per
request.

Defaults are materialized when data is *created* — a new case, a new
instance — into the row itself (§5.2). Nothing fills a value at read
time, so a field a host cleared with `Empty(path)` stays cleared, and a
definition change that adds a default never silently rewrites persisted
data (DESIGN §3, invariant 8).

Instance rows are the one place where a key taken from case data indexes
anything: `instances.index`, a per-collection `map[instanceID]int`
bounded by that collection's committed size, mapping stable IDs to row
positions rather than to nodes. Definition names never appear in it,
which is the scope of §7's claim about name-keyed execution. Nested
collections belong to the instance that declares them, so
`shareholders.42.addresses.7` is two rows, and the inner one is not
reachable from another outer instance. Removal never reuses a stable ID
and never renumbers the survivors, so a host's saved path keeps
pointing where it pointed.

Instances are addressed by stable ID as an ordinary path segment,
matching DESIGN §5.1 — `shareholders.42`, with no `[id:42]` syntax.
Condition closures capture only slot indexes, so swapping a frame
(committed → working candidate) redirects every getter for free — that
swap is what makes Update's candidate evaluation and Submit's final
evaluation share one code path. Because E2 keeps a collection's members
out of every condition, no guard ever reads a row: instance rows store
data, and every condition answer is computed from the base row, which
is what makes F6's one evaluation per collection the only possible
reading rather than a policy (§5.2).

### 5.2 Presence and requiredness

```text
present(n, s) = n.condPres == 0 || cond(n.condPres)(s.row)

// Consulted only when present(n, s):
satisfied(n, v) =
    n.flags.Required → nonEmpty(n, v)
    n.condReq == 0   → true                        // not required
    otherwise        → !cond(n.condReq)(s.row) || nonEmpty(n, v)
```

`v` is the slot's value in that state: its cell for an ordinary field,
its instance count for a collection (`required multiple` means ≥1
element, SYNTAX §4), and nothing at all for a plain group, whose
requiredness is exactly that it is present. A slot with no single value
— a member of a collection, or a collection nested inside one — has one
answer per enclosing instance, so §5.4's completeness walk iterates rows
and asks `satisfied` per member rather than letting the state invent an
aggregate.

Absence is what makes those two lines mean something, and E6 fixes it:
a comparison over a slot with no value is unsatisfied, `!=` included, so
`present[.kind == COMPANY]` is false in a fresh case rather than
accidentally true. Negation inherits the same answer instead of
inverting it — `!` over data lowers to `allSet(reads) && !inner` (§3.4),
which is why `required[!(.n > 3)]` does not demand a value from a case
where `n` was never entered. Absence is therefore never a hidden third
truth value inside a closure: `condFn` returns `bool`, and the third
state lives in `slotValue.set`, tested by the comparator that reads it.

Defaults are materialized into the row when data is created (§5.1), so
"unset" means genuinely without a value: a defaulted field is never
absent, and `Empty(path)` clears a value without touching the structure.
What the verb preserves is the *field*, not an empty payload — which is
why an emptied `required` field still fails (§5.4): its requiredness is
reached and answered, whereas a node `present` prunes is never asked.
That is also why `required` needs nothing but the emptiness test: the
default is *in* the row, so there is no `|| n.def != 0` to consult, and
a field a host cleared is not rescued by a default nobody re-applied. A
definition change therefore never rewrites persisted data.

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
than by searching a graph: no map, no allocation and no pointer chasing
per edge. And a slot is enqueued at most once, which fixes the queue's
bound at lowering — `img.depBound`, the slot count — before any request
knows what it is about to write. F7's acyclicity is what makes the
closure finite at all; the bound is what makes it non-surprising.

What the walk does allocate is two arrays per batch, both sized from
that bound — the visited set and the queue — plus one string per
*reported* path and none for a slot the collapse drops. So a batch's
cost is a function of the definition's size and not of what it wrote,
which is the shape §8's `BenchmarkUpdateFanout` measures.

Owners are the declarations a guard hangs off (§5.2), not the guards
themselves: a group with `present[…]` is one re-evaluated unit, and its
subtree is what §5.2 and §5.4 then walk. So `depOwn` answers "which
declarations must I ask again", and the declaration's own condition
closure answers the question.

An owner that lives inside a `multiple` declaration is re-asked *as that
collection*. F6 makes a condition written inside a collection uniform
over it, so the answer that moved is the group's — a client told about
one member's cell would refresh a column whose requiredness changed for
every row at once. And the collection is sufficient as well as neater:
E2 rejects a guard whose operand traverses a `multiple` node, so no slot
inside a collection's rows is an adjacency key, and promoting the member
cannot lose an edge. For the same reason `Add` and `Remove` mark the
collection and nothing else; a row's members carry its values, but the
declaration whose rows changed is the collection.

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

Two of those three terms need no separate computation, and the reason
is worth keeping beside the code. An options field must be re-fetched
exactly when it becomes reachable or unreachable, or when its own
condition moves — both presence answers, both already in the closure,
so "reached" is the walk arriving at the field. And `Update` contributes
no permission component at all: §3.5's masks depend on stage and role
set and on no value, an edit changes neither, and so no path's
permission answer differs across a batch. The operations that move those
inputs — §5.5's transition, §5.6's commit — carry that part themselves.

As shipped, the list is declaration-addressed (a row's ID belongs to
`Created`, never here), sorted in declaration order, and prefix-free: a
re-asked group or collection stands for its subtree, so a client matches
on path prefixes. And a superset means a superset — a field inside a subtree
`present` prunes is still reported, because deciding otherwise means
evaluating the guard, and §5.2 derives presence at read time for exactly
that reason.

### 5.4 Submit completeness

`Submit(form, workingRevision)` (DESIGN §6.3) is the operation that
publishes content, so it is the one that runs §5.2's whole answer. It walks
the submitted form's dense subtree over the *candidate* — the actor's own
copy — asking `present ∧ satisfied` per §5.2 and re-running `Validate` (§6)
against the values it holds, then compares that subtree with the committed
snapshot to get the forward diff §5.6 indexes. Defaults count as
satisfaction without implying prior commitment (DESIGN §4.1), and the content
questions are asked of the candidate rather than of the committed state:
asking the case what it already says would report a form nobody presented.

`base_revision == the committed revision` is *read* from the copy rather
than presented by the caller — the store knows what the candidate descends
from, and a guard the caller chooses is not a guard. The copy's own revision
is the number the caller does present, one per `(case, user)` (§5.1), and a
copy that has moved is refused rather than merged. The checks run in a fixed
order, and the order is a decision. Authority comes first: an actor the
definition refuses may not learn the case's revision traffic as a
consolation, so a stale number attached to a forbidden form is
`ACCESS_DENIED` (§7's "permission is enforced before anything is read",
applied to what a *refusal* reveals). Then the two numbers, then content;
and completeness before validation, because a field nobody filled in has no
hook worth calling.

An incomplete or invalid submit is `INVALID_OPERATION` *and* a result:
`Missing` carries the §5.2 gaps and `Invalid` the values `Validate` refused,
each addressed by stable instance ID so `Update` accepts it back. The
categories stay coarse (DESIGN §9) because the lists are the machine-readable
half, and a field is reported under one heading and not both — an empty
required field is a gap, and its hook is not asked whether the absence is
acceptable (E6). A hook that misbehaves instead of refusing — returns a class
the type does not hold, or rewrites a stored value — is `HOOK_FAILURE` with
no list, because no field the actor can fix is what broke.

An empty diff is a success that writes nothing (DESIGN §6.3: no meaningless
commits), reported with the revision it did not move. A commit whose diff
is decisions alone is a commit like any other, and that is why §5.9's
`Review` came out as this section's second caller rather than a new shape
in it: no completeness walk, no graft, no diff of its own, the same descent
rule on the copy that wrote the entry — the half §5.5 has to handle as a
special case because a transition presents no copy at all.

What this section now pays in full is §4.2's two effects: the `verif`
reset arrived with §5.9 and `sigEntry` invalidation with §5.10's `Sign`,
both inside the store's `commit`, because §5.6's targets index the very
diff this call writes.

What it deliberately does not ask is the attestation itself: the walk
skips a signature field's slot, which is §4.2's "excluding signature
fields" read as a rule about the walk rather than a parenthetical. A
required signature is not a field with no value — only `Sign` writes one,
and `Update` refuses to (§5.10) — so counting it would deadlock the
language: `Sign` requires a complete form first, and a completeness walk
that asked for an attestation would describe a form nobody could submit
and therefore nobody could ever sign. The consequence for this operation
is that `Submit` erases signatures it did not ask about and could not
have known about: the attestation is the case's and the content is the
caller's, and the effect is what keeps the two from disagreeing. The
gap's own kind lives on §5.5's list, where a destination is entitled to
ask for it, and the skip costs what it is: one test on a slot the walk was
already holding, in the branch where requiredness has been answered.

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

`Transition(dest, observedRevision)` (DESIGN §6.4) ships that list in the
order above, with the revision guard placed after permission and before
either content read. Permission first is §5.4's rule for the same reason:
the grant is a fact about the definition and the actor's committed roles,
so it answers without reading anything the actor may not see. The guard
then precedes the walk it may never reach, because both of the remaining
checks read case content and content the caller has not seen is not
content it can be told to fix — refusing on a condition absent from the
snapshot the caller named would send a message about a case it cannot
look at, while staleness is actionable either way.

Two of §5.4's habits do *not* carry over, and both are the stage's fault.
Completeness is asked of the *committed* state rather than a candidate:
there is no candidate, since a transition writes no content, and a draft
cannot satisfy a stage — the content a reviewer answers for is the content
the case holds. And it is §5.2's engine-level `present ∧ satisfied` alone,
with no `Validate` re-run: `Submit` is where a stored value is asked
whether it is acceptable, and it asked on the way in. The second habit is
the empty diff. A transition always commits, because §5.1 counts the stage
among the semantic committed changes and `commitRecord` carries the stage
per entry: the history entry a transition writes has no changes and a
different stage from the one before it, which is what lets a replay land
the case where it is.

`Affected` for a move is the paths whose *visibility* changed, and it is a
definition walk rather than a data walk. A condition never reads the stage
(§3.4's closure takes one row), so nothing about presence, requiredness or
values moves when the case does: only the §3.5 fold takes the stage as
input, through that stage's overlay. So the answer compares one permission
word per slot between the two stages and reports the topmost difference,
collapsed by §5.3's rule, and it is skipped entirely when no overlay any
held role resolves differs between the two — the common case, since
transition permissions are not per-node (P1) and most definitions stage no
grants at all. It is the acting actor's list, for the reason §5.3 gives: a
second actor's answer is a second list, and whom to notify is DESIGN §7's
`OnCommit` question.

One line of §5.4's rebase rule is load-bearing here in a way it was not
there. A commit moves its author's working copy forward only when that
copy descends from the revision the commit replaces. For a submit the
condition holds by construction — its own guard compared the copy's base
with the case's revision — so §6.3's rule reads plainly. For a transition
it does not: the caller presents the *case's* number and the operation
asks nothing of his draft, so an actor whose copy was left behind two
revisions ago may legitimately move the stage, and a rebase that followed
would relabel his draft as descending from a state it never saw. His next
submit would then pass the very guard §6.5 exists to make refuse, overwriting
a commit he had not seen. Lineage is history, and a commit may extend it
only from the state a copy stands on.

All three of the `require` line's conjuncts are paid now. The completeness
half is §5.4's, and the second is `verif` units APPROVED:
a required form's governing units — the form itself, and no nested
declaration, because F3 says the outermost `verifiable` governs — must
stand approved, and §5.9's `pendingUnits` names the ones that do not. They
join as further gaps on §5.4's list rather than as a new shape, since the
question is already "what does the destination still want", and a client
that was shown one list to render now has to reconcile two. A gap of these
two kinds is a value or a decision, and the destination's sentence counts
them apart: three fields and one approval is not six of anything,
because a reviewer's caller cannot fix a missing value and an applicant's
cannot approve. A blank `require` adds no approval question at all: the
scan runs inside the same `for f in bits(requires)` loop as §5.4's
completeness pass, so a destination that asks nothing of no form — every
stage of the SYNTAX §7 example's `draft`–`correction` loop, which is what
`BenchmarkTransition` walks — pays nothing for the conjunct, and §5.4's
`gap == 0` fast path stays the fast path it was priced as. Where a form is
required the scan is a second pass over the same span of slots, reading
`verif` from the table the walk already read requiredness from. It is short
and cache-warm rather than free, and §8 prices the walk it rides: a gated
move against a scratch copy with the scan's answer dropped came out inside
the spread of two runs of one copy.

The third conjunct is `Sign`'s and §5.10 states it; what belongs here is
only what the destination asks. A signature field of a required form that
`required` marks, whose entry does not stand for the content the case
holds, is a gap of the third kind, and it joins the list after the fields
and the units because it is the only one of the three a caller clears by
signing. The sentence counts them apart for the reason §5.4 counts a field
and an approval apart — three fields, one approval and one attestation is
not five of anything, and the hand that signs is a third hand — and the
list stays in the order a client works through it: type, be judged, sign.
The scan runs over the form's own signature range, which lowering groups
densely by owner (§5.10), and requiredness is asked with §5.4's `asked`
rather than a fourth read of the bitmask: the walk visits at most one
entry per field the declaration groups, never one per slot the form has.

A destination that asks nothing of signatures does not reach the conjunct.
The range is empty wherever no field declares one, which is every form of
the SYNTAX §7 example, so `BenchmarkTransition` walks the
`draft`–`correction` loop over a corpus where the third scan answers
before it starts — the row §8 lists is unchanged by this conjunct, and
Appendix A's trace asserts the emptiness instead of assuming it. Its
second variant adds one word of the same definition to make the same step
refuse, which is the claim that the scan is a rule and not a rendering.
`OnCommit`, step 7 of that trace, still waits for the reason it waits
everywhere else (§6, DESIGN §10's milestone 6).

### 5.6 Commit effects precomputed

Verification reset and signature invalidation are triggered by
committed content change (DESIGN §4.2), and are part of the commit
(DESIGN §6.3). Because `verif` and the owning `sigEntry` are stored per
slot, the commit diff maps to reset and invalidate targets by direct
indexing: the whole question "what becomes stale when this field
changes" is answered at lowering, in O(1) per changed slot. The one
target that is not a slot's own field is the signature range, and it is
answered at lowering too — a form's signature fields are one dense span
in one array, bounded by an anchor stamped on every slot (§3.6, §5.10),
so a changed slot says which form it belongs to and the mapping reads
that form's fields without walking a parent chain at commit time.

Both effects run. They are two implementations of one interface, driven
over the diff the commit is writing, and the driver is what decides which
entries an effect is shown: an effect maps targets from the entries it is
handed and never asks what kind of entry it is looking at (§5.10 states
the pair, and why one driver rather than two rules inside the commit).

The diff being indexed is a forward one: which declarations moved, at which
instance, and what each holds now — never the old value, which the previous
entry already carries (§5.1). It has two entry kinds *of content*, and they
are the same distinction §5.3 draws between a write and an `Add`. A value
at a path, whose payload may be absent because a committed `Empty`, or a
removed row, has something to say. And a collection's new instance count,
because a row appearing or vanishing is a change to the declaration's rows
rather than to any one value. Nesting needs no special case: the instances
a comparison visits are the union of the two states' rows, which is what
lets the removal of an outer row say anything at all about the rows that
left inside it.

The third kind of entry is not content. A review decision names a governing
unit rather than an address, and carries that unit's state, author and
comment (SYNTAX F4, §5.9). It exists because a table holding one active
decision per unit cannot answer "who approved this, when, and what did they
say" after the next reviewer replaces it, and the commit record is the only
place with room for the trail. It is written by the commit and read back
by §5.11, which is where the question this kind exists to answer turns out to
have an answer at a revision the state moved past long ago.

The fourth kind is the other act, and it carries the third discipline to
its limit: a signature entry says *which field* was attested and nothing
else, because the signer is the record's `by` and the content the
attestation binds is the record's `revision`. A reference travels with
it, and it is the only part of a signature the state does not need to
answer a question with — which is the reason the entry exists at all,
since the table is overwritten by the next hand and an erasure writes
nothing (§5.10). Its `path` is empty for the reason a decision's is, and
for a second one: S1 puts a signature field directly in a form, so there
is no row to name. It is not mergeable either, for the reason §5.8 gives
twice now: an attestation belongs to the case and never to a draft, so a
sync neither carries one across nor resets one, and what a merge moves is
values.

That is the whole reason the kinds have to be *distinguished* and not
merely allowed. Two consumers read a diff besides the history that stores
it, and they read it in opposite directions. §5.8's merge takes a cell
only from a value entry and a row list only from a rows entry, which is
where the distinction stops being cosmetic: a `verifiable` **field** is
both a cell slot and a governed node, so a pass that skipped only rows
entries would resolve the decision's slot against the base state and
answer "what did the case hold here" with "who approved it" — and an act
landing over a draft would then offer a conflict about content nobody
changed. §4.2's two resets, run by the commit itself, read the same two
kinds and no others: a commit may not unmake the act whose record it is
writing, while a row appearing under a unit is exactly the change that
ends a decision. Both tests are on the kind rather than the payload,
because neither act carries a path to test, and both are in the driver
rather than in the effects — which is what makes a fourth kind safe to
add: it arrives at two mapping loops that had already learned to filter,
and neither effect is told it exists. The last consumer is the record
itself: §5.11 renders its entries for a host to address, and the same section
folds the whole list back into a state, which is what turns §5.1's "forward
diffs, replayed" from a description of the format into a claim somebody
checks.

### 5.7 The view walk

`View(paths...)` (DESIGN §6.1) is the read of §5.1's frame and §5.2's
answers, shaped for a client: one snapshot, the selections the caller
named, and the mask of the actor asking. It is the operation the other
three exist to serve, so it is where most of the rules above become
observable — and the only one that runs *host* code while changing
nothing, which is what §6's contract and §7's last property are for.

```text
View(actor A, paths P, case at stage s, committed frame F):
  vis[n] = mayView(A's roles from F, s, n)  // O(slots), folded once
           || any descendant of n is granted            // §3.5, §6.1
  if P == {}:                             // the whole visible case
      if vis[root]: validate(root, filter = vis)
      one node per top-level form still standing
  else:
      kept = {}
      for p in P:
          r = resolve(p)                          // §3.9
          if !r.ok && vis[r.slot]: NOT_FOUND      // no such path
          if !vis[r.slot]:         ACCESS_DENIED  // not yours to see
          kept = merge(kept, r)     // containment, caller order
      for sel in kept:
          validate(sel.slot, filter = vis)        // once per scope
          node(sel.slot, sel.prefix)
  node(n, p):
      if !vis[n] || !reached(n): return nil       // both prunes, at the top
      path = addr(n, p);  kind = the declaration's word
      value = cellValue(n, p)                     // fields only
      reviewable = verif[n] == n && ! endsAt(p, n) // the unit, not a row (§5.9)
                 && mayReview(A, stage, n)         // and the caller may decide
      review = reviews[verif[n]]                   // the decision, if any
      signable = sig[n] ≠ 0 ∧ maySign(A, stage, n)  // shape, then mask (§5.10)
      signature = sigTable[n]                      // dense by slot (§5.10)
      options = fetch(n)                          // once per hook per call
      children = n's rows, in data order, or n's declaration children
```

`vis` is computed before any path is resolved: an actor's view is a
property of the definition and their roles, not of what they asked for,
and the fold is the one `BenchmarkPermResolve` measures. Containers are
marked by what they hold rather than only by their own grant — a form the
caller may not `view` by name is still rendered when something inside it
is granted, because §6.1's answer is a tree and the path to content is
part of it. An actor with no grant anywhere is answered with nothing
rather than refused: invariant 7 is about *content*, and an empty result
discloses less than a message about a form they were never in.

Then both prune rules live at the top of `node`, before anything is read,
fetched or built — §5.2's absence, and the mask `vis` just computed. The
engine prunes rather than flags, and the difference is not cosmetic. A
flagged node would be built and filtered by its caller, so an options
provider would be consulted and a host rule would run about content the
caller may not see: the fetch and the rule are two ways a view leaks even
when its output does not. Pruning costs nothing the walk would not
otherwise pay, because the answer at a node is what stops its subtree —
and `children` therefore filters nothing, which keeps the one place the
rules are stated the one place they can be forgotten about.

The consequence worth stating for its own sake is what a *selection* of
absent content means. `View("application.internal")`, while the guard
holding `internal` is closed, is not a refusal and not an error: the
subtree is not part of the effective state (§5.2), so the honest answer
is no roots. A client that named a path it may see, in data that does not
currently include it, is told nothing — the same sentence the form itself
says. Only a path with no permission behind it refuses, and §7's last
property fixes which of `ACCESS_DENIED` and `NOT_FOUND` a probe hears, so
that grant status and row existence each stay unobservable through the
other.

The snapshot is read through `viewed` and not `working`. The two differ
by one verb: `working` *creates* a per-user copy on first use, because an
edit is about to write into it, and a view must not — a read that
allocated state would let rendering a form grow the case, which is
DESIGN §9's no-persistent-effect rule applied to an operation that
changes nothing, and at ten thousand viewers it is ten thousand rows for
nothing. So an actor with no copy is answered from the committed
snapshot, and the number reported for it is 0: the same one §6.2 tells
that actor to present as `base` when they do write, because the copy they
then get is a fresh one. The state is cloned on the way out regardless —
the store's clone rule stated without exception, since a walk that "only
reads" today is one memoization away from writing through a window into
stored data.

What the read is *of* splits two ways, and the split is invariant 6.
Values, conditions and requiredness come from the actor's own draft: a
form is completed before it is filed, and the client rendering it needs
the answers that decide what to ask for next, not the ones that held
before the actor typed. Roles and stage come from the committed case, so
a draft that assigns a reviewer grants that reviewer nothing, and the two
revision numbers one result reports are deliberately two different things
— the case's and this copy's.

The two review fields are read from two different places, and that is the
one part of `View` worth stating twice. `Review` is the case's decision,
taken from the committed snapshot, while the value beside it is the
caller's draft (§5.9): an applicant with unsaved edits sees the verdict
about the content the case *holds*, and both statements are true because
they are about different content. `Reviewable` is the only field on the node
computed against the caller rather than the definition alone, and it answers
a narrower question than "may review here": it is true only at the node a
decision may actually be addressed to. A row of a `verifiable multiple`
holds the same slot as the declaration above it, and `Review` refuses that
address, so a flag that offered it would be an invitation to a refusal
(§5.9). Both fields obey §5.2's prune rule rather than being withheld
beside content: a node that is not there reports no decision and no offer
to decide, and no flag says which half is absent.

Two node fields are read at the node they are printed on, and both are
worth the sentence they cost. `CanEdit` is true of a form or a group
granted `edit`, because that is where the grant was written: the mask
belongs to the declaration and not to a widget, and a client that wants
to know whether it may send a batch naming a container asks the
container. `CanSubmit` is read the same way even though P3 (SYNTAX §5)
lets a `submit` grant be *written* only at a top-level form, so no field,
no group and no `deny` at either can carry a submit bit of its own; the
fold of §3.5 is total, which is what makes a member's mask carry its
form's bit and nothing else. That equivalence is what
`TestSubmitMaskIsTheFormsMaskEverywhere` holds open, because the cheap
reading of P3 depends on it. The value is on every node rather than only
the root because the node a client holds is usually a field and the
question it is asking is about a button.

`Kind` and `Value` are two vocabularies and the node carries both,
because the client needs both. `Kind` is the *declaration's* word:
`form`, `group`, `role`, or the class name of a field's base type — so a
`money` field reports `decimal`, and the custom type's own name stays in
source the host already has instead of becoming a second table a client
must ship. `Value` is the *row's*, typed, and for an `options` field the
two disagree on purpose: the kind is `options`, the value is the chosen
option's text (§3.3). A form, a group and a collection as declared hold
no value and answer that with `KindNone` rather than with a zero.

That is also why `Multiple` exists. A view that reports an empty group
and an empty collection identically tells a client "there is nothing
here" when the sentence it means is "add a row", and the distinction
cannot be recovered from the data, because §5.1's rows are the only
evidence of it. `Value.Kind() == KindNone` is the absence test: an unset
cell stays distinguishable from one holding the empty string, and no
`HasValue` flag is needed to state the same fact twice. Requiredness is
asked per node through §5.2's `asked`, which reads the draft, so a
`required[cond]` field's demand appears and disappears as the actor's own
values move.

`Options` is fetched once per binding per call, keyed by `hookID`, and the
cache is what keeps a collection honest: a field with two hundred rows is one
host call and two hundred renderings of the same list, because the binding is
by field path and the provider was never offered a row. The text handed to it
is that binding, with no instance in it, beside the caller's own snapshot — so
a provider that consults case data is asked a question about the case, which
is what §6's mask then answers. A missing provider is
`HOOK_FAILURE` naming the binding rather than an empty list, and a provider
that errors is the same with its cause attached; both refuse the whole view,
because the alternative is a form on screen with a dropdown that cannot open.
The cache is one call and not one case, so a host that changes its catalogue is
asked again rather than showing a stale answer until something else invalidates
it.

The scope's validation report is asked once per selected root as a subtree
walk, before its first node is built, and it is asked *through the mask*:
`validate` takes the visibility array as its filter, so a host rule never runs
over content the caller may not see. Two leaks close that way — a
`FieldProblem` whose path names a withheld field, and a `HOOK_FAILURE` that
refuses one actor's view because another actor's unviewable value happens to
break §6's fixed-point contract. Submit's walk is unfiltered, and the asymmetry
is the point: the actor committing content holds the grant for it, and the
operation entitled to discover a broken hook is the one about to publish what
the hook touched. A selection addressed to one instance therefore does hear the
rule about its siblings, because the report is a subtree answer indexed by
address and not a per-row question; the answers about rows nobody selected are
dropped by never being looked up.

Deferred no longer, and the record of the gap is kept because its shape
explains the shape of the answer. Both halves of §6.1's node fields are
here: `Reviewable` and `Review` print §3.6's `verif` answer, and §5.9
says where each is read from and which nodes may not carry it;
`Signable` and `Signature` print the `sigEntry` answer, and §5.10 says
which of the two conjuncts the view reports and why the other one is not
asked of a rendering. `Options` is keyed by a field path rather than by
a field instance because the binding is a
declaration's, and the `CaseContext` handed beside it is a case's snapshot
and not a row's (§6). And the question §6.5 leaves open about a draft
whose committed content moved is answered in §5.8, not here: `View`
renders the draft it is handed, unchanged in shape whether or not that
draft is behind the case, and `Rebase` is the operation that changes what
the draft holds.

### 5.8 Synchronization — `Rebase` and `Discard`

DESIGN §6.5's rule is that committed change never rewrites a draft by
itself, and that a draft the case has moved past cannot submit until
somebody brings it forward. `Rebase` is the bringing-forward and
`Discard` is the other exit, the draft given up rather than reconciled.
Neither commits anything: invariant 2 counts commits, and what a rebase
moves is a copy, whose number the caller already holds (§5.1's working
revision). Both are the package's and not the host's, because the
comparison they settle needs the case's history and every actor's draft,
and a host holding those would be holding the store.

**The third version.** Every operation before this one compares two
states — a candidate against the committed snapshot, an edit against the
draft — and a merge needs three, because "which side changed this" is a
question about a starting point and not about a pair. The middle version,
what the copy descended from, is stored nowhere: history records new values
and never old (§5.1), and the store materializes two states — the case as it
is, and each draft as it is — which is one short of what a merge asks. So
the third is *read*, exactly and without a replay: a commit entry says what
the case held immediately after that commit, so the newest entry at or below
the copy's base is the base. The answer is that entry's last word and not
the first word found, which is why the scan runs backwards. The window the
copy has not seen needs no search either, because history is dense (§5.6):
one binary search for the first revision past the copy's base divides the
two halves, and the entries in the second are the case's newest word on
every value and every row list.

Three questions have no entry to read, and each has an answer that comes
from the definition or the empty list rather than from the draft:

- A collection no commit at or below the base moved had **no rows**. Rows
  come only from a commit that makes them, so the ledger's silence is the
  empty list, and §5.6's entry-side list of IDs is what makes that statement
  about membership rather than about a count.
- A cell the ledger never wrote, in a row the base did have, holds what
  creating that row materialized: the **default the declaration states**
  (§5.1). It is asked of the definition and not of the draft, because a
  draft's own value read back as the base would make every edit the actor
  typed look like no edit at all, and the merge would report a clean
  synchronization of a draft it had never compared.
- A cell in a row the base did **not** have has no value at the base, and
  that is answered by the row's list rather than by a search. This is why
  the walk is two passes, lists first: membership is what says which cells
  may be asked about at all, and a cell in a row the case did not yet have
  is not a question the ledger can hold the answer to.

What makes the answer right is neither of the walk's two stops. It runs
backwards, and each branch refuses a second word about a question it has
already answered, so the newest entry at or below the base is what stands:
history holds forward diffs (§5.1), and writing an older entry over a key a
newer one already answered is that diff read backwards. Stopping when no
question is open is only the cost bound, and the ordering above is what
makes the bound hold. Without it, a case whose rows churn — one row issued
and one removed in every commit — leaves one cell permanently unanswerable,
and every rebase rereads the case's whole past to prove none of it holds
the answer; §8 runs the merge in both orders and prices that reading at
roughly seven times the cost of the merge itself. What no ordering can
bound is a collection the case has never moved at all: its answer is the
ledger's silence, and silence is only established at the beginning of the
ledger. Those are the few questions, and this walk answers them exactly
rather than approximately.

**The merge.** Mechanical, three-way, and never a combination: where one
side did not move, the other is taken, and where both moved the operation
refuses by name. Values and row lists are asked the same four things in the
same order — did the commits move it at all, did the draft, both the same
way, or both differently.

- Commits moved nothing: the draft's content stands, including a value that
  went away and came back, which is the net effect §5.1's forward diffs
  record and the only one a merge has to honour.
- The draft moved nothing: the committed answer is written in — a poured
  value, or a whole list adopted by ID so the entries after it can find
  their rows. A committed value whose address is gone from the merged data
  is not written and not reported: its row was removed by the commits, and
  that is the change §6.5 says to integrate.
- Both sides wrote the same thing: nothing to tell anybody.
- Both sides moved it differently: a clash naming the address. For a
  collection that means the draft keeps its own rows, the set is marked
  dropped, and every value entry writing under that address is dropped with
  it — a field merged into a list nobody reconciled would be a decision
  smuggled in through a row.

Two row cases are worth naming, because a comparison of two lists cannot
see either. A row this draft deleted while the unseen commits changed what
was inside it is a loss of committed work, and the deletion winning quietly
is the thing §6.5 exists to prevent: it is reported *once*, at the
collection, and the nested lists and the fields under the deleted row are
suppressed as the same decision, so one removed holder does not name sixty
addresses (§5.7 renders a container as one node, and a refusal follows the
container for the same reason). And a row the case deleted that this draft
also deleted is agreement, and the converged branch says so without asking
whether either side meant it.

A merge that adopts a row takes the case's ID with it (§5.1's stable paths)
and hands the draft the case's allocator along with its content — the same
reconciliation §5.4 makes going the other way, and the reason a post-rebase
`Add` cannot issue a number the case already published.

**What a rebase writes.** Nothing, unless the whole merge is clean. The
merge builds on a clone and the save is reached only with an empty clash
list, so §6.5's "a failed/conflicting rebase leaves the previous working
state intact" is the shape of the call rather than a rollback, and
`RebaseResult.Base` keeps reporting the revision the copy is actually at:
a result implying a synchronization that did not happen would be a lie
about the one number this operation exists to fix. A clean one moves two
numbers. The copy's revision moves because a second session of the same
actor holding the old one could otherwise save a batch built against the
*unsynchronized* draft over the merge — invariant 4's failure with a nicer
name — and the base moves inside the lineage the store owns: `saveWorking`
accepts a base no lower than the copy's own and no higher than the case's
revision, and anything else is `SCHEMA_MISMATCH`, which is §4's drift
question asked where a snapshot is written rather than where it is read.
A rebase of a copy already at the case's newest revision is not a merge and
not a write: it answers with the numbers the caller has, and §8's floor row
prices that as the read of a draft.

The word carries two meanings here, and the difference is worth stating
once. §5.4's "rebase" at submit is the lineage move by itself: the commit
sets its author's copy to the revision it just made, and §5.4's condition
on that move — the copy must descend from the revision the commit replaces
— holds there by construction, because submit's own guard compared the two
a moment before. `Rebase` is the move asked where nothing has been compared
yet: a copy whose content is not the case's, which is the only kind that
needs a merge and the only kind that can refuse one.

**What a refusal says.** `Conflicts` arrives beside the `CONFLICT` refusal
and never beside a success, so a host reads one source of truth about
whether work is left; the paths are §5.1's addresses, in the order the merge
found them. `CONFLICT` is DESIGN §9's category that no earlier operation had
a reason to produce — a stale submit is `STALE_REVISION`, a forbidden one is
`ACCESS_DENIED`, and nothing else in the engine had two authors to disagree
about — so this is the call where the host learns which fields it must show
a human. The mask decides what may be *said* as well as what may be
shown: a clash at a slot the actor may not view is counted and named by the
form alone, never by the collection, row and field inside it, because §7's
property 7 is a claim about refusals as much as about renders, and a message
printing `application.holders.17.who` has just told a caller that row 17
exists and that somebody changed a value in it. The count of hidden clashes
does appear in the message, with the one exit that needs no grant — to
`Discard`, which throws this work away without describing it.

`Discard` is that exit, and it is a small one: the number the caller was
shown, and a delete. It is not a commit, not a revert, and not a reset in
place: the copy is removed, so the next access clones the committed state
and starts clean. That is §5.1's rule for how a draft comes to exist at
all, stated by §5.7's `working` with a creation verb, and the promise holds
here because there is nothing left to reset rather than because this
operation undertook to keep it. Beside no copy the number decides the
answer: `0` is success, because there is no draft and §6.5's requirement is
already satisfied, and anything else is `INVALID_OPERATION`, because a
caller reporting edits it never held is this package's bug to hear about
and not the case's state to report. Beside a copy that has moved the answer
is `STALE_REVISION` naming both numbers, the same guard `Update` and
`Submit` give and for the same reason: the draft this call would destroy
may be the one another session of this actor wrote a moment ago
(invariant 4).

Deferred, all of it on the record. Reconciling two row orders is §6.5's
"not part of the initial implementation contract", and a merge that picked
one would be inventing a policy no document states: the union of two lists
needs an order, and order is which user is looking at the screen. Conflict
*presentation* is deferred beside it — the paths are the whole offer, and a
side-by-side editor is a host's. No operation calls `Rebase`: `Submit`
refuses a copy that has fallen behind with the revision to reload at (§5.4),
which is §6.5's "no implicit merge occurs during Submit" kept literally. And
a merge asks no hook (§6): it moves committed values into a draft that has
never been normalized or checked by host code, and the two calls that would
notice — §5.4's completeness at submit, §5.7's masked report at view — are
the caller's next request. `OnCommit` notification, which §6.5 says a host
may use to tell a user the case moved, waits for milestone 6's observers.
Of §5.6's two effects the merge turned out to owe nothing at all, and not
because either arrived late: a decision and an attestation each belong to
the case and never to a draft, so neither table is carried across a sync
nor reset by one. What a merge moves is values, and values were never what
either act was made about — the reviewer's own copy may be a hundred
revisions stale and the decision in it is the case's, standing or erased by
facts it does not contain, and the signer's copy no less so. That is why
both kinds are filtered positively on both passes rather than excluded by
shape: a fourth kind of entry arrives at a merge that already learned to
ask the kind, and it lands nowhere in the draft it did not change.

### 5.9 Review — a decision about committed content

```text
Review(actor A, target t, action k, observed revision r, comment c):
  roles = A's primary role ∪ committed role-typed assignments (T4)
  s, ok = resolve(t)                       // the committed frame, §3.9
  grant = mayReview(roles, stage, s.slot)  // §3.5's mask, at that slot
  if !ok ∧ !grant:  ACCESS_DENIED          // never which half failed
  if !ok ∧  grant:  NOT_FOUND
  unit = slots[s.slot].verif
  if unit ≠ s.slot:  INVALID_OPERATION naming unit        // F3
  if s.depth ≠ 0:    INVALID_OPERATION naming unit        // F4
  require r == case revision          // a result beside the refusal (§6.4)
  k ∈ {APPROVE, REJECT, REVOKE}
  k == REJECT  ⇒ c non-blank;  k == REVOKE ⇒ c blank
  cur = reviews[unit]
  k == REVOKE  ⇒ cur is standing and cur.by == A          // §4.2
  next = the decision k asks for, at revision r+1
  if next == cur: success, and nothing is committed       // §6.3
  commit(snapshot unchanged, reviews[unit] = next,
         changes = [review entry for unit])               // §5.6's reset
```

It is the third operation that commits and the first whose commit says nothing
about data. That single fact decides where everything lives: a decision is
about the *case*, so `Review` reads no draft, writes no draft, and stores its
table beside the committed snapshot instead of inside it. Three consequences
follow from that placement and none of them needs a rule of its own. `Update`
cannot write a decision, because a decision is not one of the four working
edits and not in the state an edit rewrites. `Rebase` has nothing to merge,
because the window it walks carries a `chgReview` entry whose address may name
a cell slot — a `verifiable` *field* is both a slot and a governed node — and
§5.8's two passes skip every kind that is not content, so a decision landing
over a draft moves the copy's lineage and not its values. And `Submit` grafts
no decision: it replaces a form's subtree in the snapshot, and the table is not
in the subtree.

A dense array indexed by slot is the shape, allocated on the first decision
anybody makes and shared by every reader until something resets it. Indexing by
slot rather than by unit count is §3.9's rule kept — nothing that came from
case data indexes anything — and it is what makes the two reads here one array
access apiece. The zero entry is `PENDING`, which is why a case nobody has
reviewed holds no array at all: an absent entry and a pending entry are the
same answer, and a table sized to the slots on registration would be a
per-definition cost paid by cases that never use it.

**The unit is the declaration, not the address.** F3 makes the outermost
`verifiable` declaration govern, so a `verifiable` field inside a `verifiable`
form is decided by the form and a decision addressed to the field is refused —
naming the unit that answers, because a host left to discover the target by
trial is being taught the definition by its error messages. F4 makes a
collection decided as a whole, so a path through a row is refused the same way
even when the row's address resolves against somebody's data. Both refusals are
`INVALID_OPERATION` and both name the unit, and §5.7's `Reviewable` is computed
by the same two tests: an actor with a real `review` grant at a nested group
holds a permission the engine will not take a decision with, and the view says
so instead of offering a button that cannot be pressed.

**One decision is active per unit.** A second approval is not a duplicate: it
is a replacement, and the case says whose judgement stands. So the comparison
that decides whether to commit is between three facts — state, author, comment
— and not between two states, because "the same APPROVED by a different hand"
is new information the history did not have. Revocation is the author's alone,
and it fails two ways that must not be confusable: nothing standing is
`INVALID_OPERATION` (a permission answer there would disclose that a decision
exists, and the answer the caller needs is elsewhere) while somebody else's
decision is `ACCESS_DENIED` naming its author — the author is not a secret,
both reviewers may see the unit, and without the name the second one cannot
tell "you may not" from "there is nothing to take back".

**What the decision was about** is the observed revision, and the guard refuses
a decision about a case that moved even when the move touched an unrelated
form. That is stricter than §4.2's reset and it is deliberate: a reviewer's
"approved" is a statement about the whole case he was shown, and a reset keyed
to content would let an approval stand that was made about a revision nobody
asked him to look at again. It is the first refusal in this operation that
carries a result, and the one where the result is the whole answer, because
naming the revision to reload at is what the caller needs (§6.4) — and the
case's number is not a secret from a caller who may decide here, the way a
withheld path is (§7's property 7). The refusals before it answer with an
empty result on purpose: a caller who may not decide, or who named a path
that is not a unit, is told a category and nothing about where the case
stands. The mark stored
is the revision the decision's *own* commit produces, predicted one ahead of
what the caller observed: the store's guard runs under the same lock and
refuses anything that arrived at another number, so the prediction is exact,
and reading the number back is impossible because the table is part of what is
being committed. Staleness is not stored and not computed from two numbers —
§5.6's reset is what ends a decision's life.

**The reset is a consumer, and the first one indexing was built for.** §4.2
says committed content change puts a unit back to `PENDING`, and DESIGN §6.3
says it is part of the commit — so it runs inside the store's `commit`, after
the revision guard and before either write, and not in each operation that
might remember it. The diff the same call is writing is mapped through
`slots[n].verif`: one array read per entry, no walk, no dependency graph, and
no work at all for a commit that touched nothing under a unit. Two kinds are
told apart, and both readings are load-bearing. A `chgReview` entry is skipped:
a commit whose changes are decisions must not reset the unit it just decided,
which would unmake the act the entry records. A rows entry is *not* skipped: a
row appearing or vanishing under a verifiable group is that group's content
changing, and its slot's `verif` is the unit that governs it. The table is
copied once and only if something resets, so the ordinary commit hands the
stored slice forward untouched.

Resets are not recorded. History says who decided what and when, and the state
of a unit at revision *i* is derived by replaying the entries between the
decision and *i* — a decision that no commit's diff erased is standing, and one
whose revision a content change passed is not. Recording the erasure as well
would store the same fact twice and give the two a chance to disagree.

`View` prints the decision from the committed snapshot while it prints values
from the caller's draft, which is §6.1's split at its most visible: an applicant
with unsaved edits sees the verdict about what the case *holds*, and both
statements are true because they are about different content. Decisions are
disclosed to actors who may not make them — a rejection with no reader is a
wall, not a refusal — and the unit's report is carried by every node in its
subtree, so a client rendering one field can show the mark beside it without
asking a second question. A node under no `verifiable` declaration reports no
unit, which §5.7's rule about absence states as a zero value rather than a
flag.

What `Review` does not pay, on the record: no `Affected` set, for the reason
§9 gives and in the company of `Submit`'s missing one; and no `OnCommit`
notification, which waits for milestone 6's observers. What it does pay is the
history entry, and §5.11 reads it back in this section's own shape: the same
`ReviewInfo`, at the record's revision, with the hand the record's envelope
names. It is written now because a history that cannot say who decided what is
one the milestone that asks would have to rewrite, and because a reset writes
no entry beside it — the trail holds both acts, so the state is free to keep
only the standing one (§4.2). Its §5.10 counterpart, the `chgSign` entry, is
written for the same reason and read the same way.

### 5.10 Sign — an attestation about committed content

```text
Sign(actor A, field f, observed revision r, evidence e):
  roles = A's primary role ∪ committed role-typed assignments (T4)
  s, ok = resolve(f)                        // the committed frame, §3.9
  grant = maySign(roles, stage, s.slot)     // §3.5's mask, at that slot
  if !ok ∧ !grant:  ACCESS_DENIED           // never which half failed
  if !ok ∧  grant:  NOT_FOUND
  if sig[slot] == 0:  INVALID_OPERATION naming the path    // not a signer
  if s.depth ≠ 0:     INVALID_OPERATION naming f           // S1
  require r == case revision          // a result beside the refusal (§6.4)
  entry = sigs[sig[slot]]                       // §3.6's resolved pair
  entry.assigned ≠ 0 ⇒ committed value there == A.user     // S3
  entry.role     ≠ 0 ⇒ entry.role ∈ roles                  // S3
  gaps = incomplete(owner(f)) ∪ pendingUnits(reviews, owner(f))
  if gaps: INVALID_OPERATION, both kinds counted, gaps in the result
  next = {by: A, at: r+1, ref: e}
  if cur.by == next.by ∧ cur.ref == next.ref: success, nothing committed
  commit(snapshot unchanged, sigs[f] = next,
         changes = [signature entry for f], op = SIGN)     // §5.6's effects
```

The guard order is §5.9's, copied for the reason that section states: the
two questions about the definition come first and answer with nothing but
a category, the observed revision is the first refusal that can name the
case's own numbers, and what the signature would attest to is asked only
once the caller's freshness of view is known. `Sign` is the fourth
operation that commits and the second whose commit says nothing about
data, so it shares `Review`'s shape at every step that is not about the
verdict — the store's commit call, the empty-`Affected` deferral, and the
rule that a signature does not move the stage. What it does not share is
`ReviewResult`'s shape: `Review` refuses a caller who was not asked to
decide, which is a fact about him and not about the case, while `Sign`
refuses a caller whose form is not ready, which is a list of paths, so the
result carries §5.4's `Missing` and the refusal counts the two kinds of
gap it can name (§6.4's "result beside the refusal" applies to the
refusals that have something to say).

Two conjuncts answer "may this hand sign here", and only one of them is
the mask. `permSign` at the field's own slot says the act was granted: P3
puts a `sign` target on a signature field, so that slot is where a
targeted grant is written and §3.5's fold has already carried the
inherited bits down to it — one read answers both, and no ancestor is
consulted a second time. `sigEntry` then says whether this is the hand the
field was opened to, and that is a separate statement in a separate
declaration: `allow sign X` and `signature(Y)` are not required to agree,
and S3 states each half's answer as a question about *who* — the first
eligible holder of a role, or the user a role-typed field names — which is
exactly the part the front end cannot check. The mask alone is not enough,
because `all` is inheritance from the case root and a definition may grant
`sign all` to a role no signature field names; the field's own conjunct is
what refuses such a holder. Both read the committed role fold (T4), so an
actor who reaches a role through a role-typed field holds it here as
everywhere else.

The shape test between them is load-bearing rather than tidy. A slot with
`sig == 0` declares no attestation to make, and it is reached through the
same inherited bit that `all` grants, so a runtime that asked only the mask
would treat a grant over every signature in a case as a grant over every
field in it. The answer is `INVALID_OPERATION` and not `ACCESS_DENIED`, for
§5.7's reason: the mask admitted the caller, so naming the shape of the
path discloses nothing the grant did not already. S1's corollary is the
other definition question — a path through a row names an instance, and
§4.2 binds a signature to form content, which is the whole form and not a
member of it — and it is unreachable by the front end's own rule, kept for
the reason §5.9 keeps the row test beside it.

What must be true of the content is DESIGN §6.4's own words, and both
halves are already written for §5.5: `incomplete` over the owning form and
`pendingUnits` over its `verifiable` declarations, asked of the committed
state and of no draft. A signature bound to content nobody published would
be one statement about two things, and each actor's attestation would then
be about a different form. The consequence is worth stating plainly because
it surprises a host: signing a *verifiable* form requires its unit
APPROVED first, so `Sign` is unreachable until `Review` has run — which is
what §4.2's "all effective verifiable units must be approved" means as an
operation rather than as an invariant. The list a refusal carries is
§5.4's, in §5.4's order, with the field's own absence left out of it:
signing is how this call answers that question, and a refusal that named
the attestation would tell the caller to do the thing they were doing.
No `Validate` hook runs, because committed content passed the gates
against every byte of it at its own commit, and re-asking a host whether
accepted content is acceptable would make an attestation depend on when
it was asked rather than on what it attests to.

`evidence` is the host's words about what its ceremony produced — a
document, an envelope, a verification ID. The engine stores the reference
and interprets nothing, which is §1's minimal surface applied to a
signature: the *authentication* half of §4.2 belongs to the host and its
verifier, since the engine holds no secret material and a printed image is
presentational, and the case holds the fact that an authorized hand
attested at a revision. A blank reference is accepted and stored as blank,
because the language has no expression for one and refusing a signature for
want of a pointer would be inventing a rule §4.2 does not state.

So the stored entry is three fields and no more: whose hand, which revision
the attestation's own commit produced, and the caller's reference. There is
no bound-revision field, because the number says it — content bound is
`at-1`, the state the commit descended from — and no content digest, which
is a decision rather than an omission: §5.6's erasure is what makes an
entry's presence mean "this content", so bytes beside it could only agree
with the entry or contradict it, and a digest would be the engine hashing
the case's data for no consumer, per commit, to answer a question one array
read answers. `at != 0` is therefore the whole of validity for current
content, and there is no `Stale` flag and no code for "invalidated": the
effect removes the entry, so an attestation that outlived its content is not
representable. "Never signed" and "invalidated" are one answer through the
interface, and the difference between them is history's to tell.

One entry per field, and replacement rather than refusal, is §4.2's "at most
one effective signature per content version" read as the state invariant it
is. A second authorized hand on a field open to a role is not a second
signature — the field's answer after both hands is one name — and the
displaced act stays in the record as the `chgSign` entry that erased it.
Refusing the second hand would lock a role-open field to whoever asked
first, in a definition that said nothing of the kind. Revocation is not
offered, and the asymmetry with `Review` is the language's: §4.2 lists
REVOKE for decisions and not for signatures, and taking back an attestation
without withdrawing the ceremony that made it is a statement the engine
cannot make on a host's behalf. The same hand repeating the same reference
is already true, so it succeeds and commits nothing (§6.3, exactly as
§5.9's repeated decision); a *different* reference is a different statement
about what the ceremony produced, so it commits at the same hand and the
same content.

The table sits beside the committed state, dense by slot, and mirrors
`reviewTable` in every respect: nil for a case nobody has signed, one entry
past its end being no signature, allocated to the slot count on the first
attestation and copied once per commit that changes one. Density is the
same trade §5.9 states — `sigAt` is one index, and the slots the numbering
puts in a form's range are the only ones read — and the copy is what keeps
the store's replace-never-mutate rule while the effect clears entries.
`sigReset`'s mapping is two array reads and a span: the anchor stamped on
the changed slot (§3.2, §3.8) says which form's content moved and
`sigSlotsOf` says which fields that form declares, in declaration order. A
form that declares none has an empty range, so a definition with no
signatures costs two reads and no copy, and the range's size bounds the work
of one that has them: the question is answered by the signatures a form has
and never by its fields. A role-typed field's value is content, so
reassigning the reviewer erases the attestation that value authorized and
moves the right to the new name — not a rule this effect adds, but §4.2's
rule applied to a form whose content includes who is in charge of it.
`BenchmarkSigReset` prices the pair of rows, standing and empty.

`View` reports two facts per node, and they are asked differently.
`Signature` is read from the committed snapshot through the same index
`Sign` writes, beside values that come from the caller's draft — §6.1's
split at its second most visible place, since a hand vouches for what is
published while somebody edits what is not. `Signable` is the mask and the
shape and nothing else: the entry's identity conjunct is about a *person*,
and a view is not asked to guess which person is reading it, so a `sign
all` holder is shown every signature field as signable and is refused by
the conjunct the view does not report. A node whose slot declares no
signature is never signable, whatever the grant, which is the shape test
again on the read side, and an erased attestation leaves the field
signable because nothing about the definition changed.

What `Sign` does not pay, on the record: no `Affected` set, in the company
of `Submit`'s and `Review`'s missing ones for §9's reason; no `OnCommit`,
which waits for milestone 6's observers; and no verifier interface, which
DESIGN §9 keeps as a deployment's. What it does pay is the entry, and §5.11
reads it back for the reason §5.9 gives: "whose hand stood at revision 12, and
against what artifact" has no other answer once a second signer or a content
commit has moved the state. Appendix A's step 8 asks that of the conformance
corpus, where a later submit erases the attestation and the trail is the only
place it still is.

### 5.11 History — the account the entries were written for

```text
History(actor A, cursor c, limit L, case at stage s):
  d, rec = the case's record and definition   // NOT_FOUND; §4's drift refuses
  if L < 0:                    INVALID_OPERATION  // a page has no size
  roles = A's primary role ∪ committed role-typed assignments (T4)
  vis[] = viewable(img, roles, s)             // §5.7's fold, once per call
  page  = rec.window(c, L)                    // arithmetic on a dense index
  for com in page:
    out = {com.revision, com.at, com.by, com.op, com.stage}  // the envelope
    for ch in com.changes:
      if !disclosed(img, roles, s, vis, ch): withheld++; continue
      out.changes += entry(img, com, ch)      // from the record alone
  next = last revision + 1, or 0 when the page reached the head
```

**Why a section, and not a store method.** Four sentences in this document are
claims about a reader. §5.1's, that a commit records new values and never old
ones; §4.2's, that a reset is not recorded because a replay derives it; §5.6's,
that the two verdict kinds exist because history is where an erased decision and
an invalidated attestation survive; and §5.9's and §5.10's, that neither
operation owes a `Stale` flag because the state erases what moved and the trail
keeps the account. None of those was checkable until something folded the
entries back into an answer, and this slice is that reader: the read, the fold,
and the tests that turn the four sentences from hopes into claims.

**The page is the cost.** `window` is arithmetic on a dense index — revision *i*
is the entry at slot *i-1*, and creating a case appends nothing, which is why
revision 0 has no entry — so a call costs the mask fold, bounded by the
definition, plus one render per entry it answers with. A page of twenty records
from a thousand-commit case costs 2.3 µs and the whole trail 87 µs: 50× the work
for 50× the entries, at 87 ns a record however far back the cursor starts, and
nothing in the read searches, copies or rebuilds the past. §8 has the rows.

**Paging is forward and total.** `c` is the first revision to read and `L` how
many to answer with, and each has one word for "no preference": `c` of 0 is the
beginning, because there is no entry at revision 0 to start at, and `L` of 0 is
everything remaining. A negative `L` is refused rather than read as 0 — a caller
who meant everything has a word for it, and one who meant the newest ten is
about to be handed the oldest. A cursor past the head is an empty page and
not a refusal, because the trail is dense, so "nothing at 900" and "nothing
left" are the same fact. `Next` is the revision to ask again with, or 0 when the
page reached the head. That zero is also the cursor for the beginning, which is
the one trap the contract has: a loop that copies `Next` back in without reading
it reads the whole history a second time. `Revision` is on the result for a
caller who would rather check than trust, and the test that walks a trail to its
end names the trap instead of papering over it with an error.

**Permission is the view's, asked now.** §7's property 7 is that permission is
enforced before anything is read, and this is a read of content that is old. The
rule is not a second account of what somebody could see *then* — such a rule
would be a disclosure the definition no longer grants — but §5.7's own fold, at
the roles the committed state carries and the stage the case sits at. A
stage-scoped subtree grant the case has moved out of therefore stops disclosing
entries it once reached, and a widened grant discloses entries the same stored
trail already held. The mask is a function of the reader and the case now; the
entries are a function of what happened.

**One question a trail has to ask harder than a view does.** A rows entry names
a collection's instances, and §5.7 shows a container to anyone it shows a child
to, so `viewable`'s reachability is the right test for it: a page that listed
addresses its reader could not address would be a page that cannot be read on.
Everything else — a value, a decision *about* a unit, an attestation on a
field — is asked at the node itself, through the same `effective` lookup §3.5's
fold serves, so a trail and a form cannot disagree about who sees what. The
cost is one array read per entry rather than a second fold. The difference
matters once, and it is worth the separate rule: `viewable` marks every
ancestor of a granted node reachable, because a container nobody may view
carrying content somebody may cannot explain where its own child came from — so
reachability alone would let an actor who may read a form's rows read the
registrar's decision *about* the form, which is content of the unit and not a
path to a row. A view is not exposed to that: it renders a verdict at the node
the grant names, and a trail renders it at a record.

**The record survives its content.** An entry the mask does not reach is
dropped and counted, and the commit it belonged to stays on the page: revision,
moment, author and operation are what an audit is made of, and §5.9's argument
about a rejection nobody may see — that a wall is not a refusal — is the
argument for a silent page being visible too. Filtering envelopes would also
withhold nothing `View` already hands over: the head revision and the current
stage arrive in every view result, and the trail's length *is* the head
revision (§5.6). What it would destroy is the case's stage history, because a
`Transition` commit carries no entries at all and would be the first record to
vanish. And `Withheld` is a count and not a list for the other half of the same
rule: that this page is not the whole story is a fact the caller is owed, which
declaration moved is not.

**Addresses, including dead ones.** Every entry's `Path` is the spine `View`
addresses nodes by and §5.4's gap reports name fields by, and a page's addresses
are the ones `Update` takes back — Appendix A's step 8 writes a recorded path
into a new batch for exactly that reason. A rows entry lists instances at §5.1's
addresses, and an instance it names may be gone: an address that stops
resolving because the row went is the truth about a past commit — the same rule
§5.9 states about a decision whose unit is gone. Rendering an entry consults no
case data, which is what lets a trail name a dead instance at all.

**The one refusal, and why a rename is structural here.** A stored entry keys
its slot ordinal and not its name, so everything above is stable only while the
ordinal space is: `compat.go`'s drift check compares each slot's *path* for that
reason and refuses a definition that renamed, moved or re-typed what a slot
holds rather than reinterpret committed history into nonsense. `History`
therefore refuses a relabel with `SCHEMA_MISMATCH`, as `View` and `Rebase` do —
the one place in the package where that refusal does real work, since a view
addresses what the caller named and a trail addresses what a past call recorded.
Purely semantic edits are read through: a permission word, a guard, a default, a
decimal scale. Those change what a value means, or who may see it, which is
DESIGN §3's reinterpretation, and not where it lives. A value renders under the
definition the case is read under now, which is the same sentence from the other
side, and it is why this section can say the trail is frozen about addresses and
deliberately not about meaning.

**Verdict entries are the reason the read exists.** A decision arrives in
`ReviewInfo` — the unit, the state, the comment — and an attestation in
`SignatureInfo`, carrying the reference §5.10 says the case keeps without
interpreting. Both take author and revision from the record's envelope instead
of a payload that copies them, which is §5.9's rule that a commit carries no
second copy of an author, read back by a caller who was not the one who made the
call. It is also what makes a retraction legible: the table answers who stands
behind a *standing* verdict, so a revoked decision renders as nothing standing
and the hand that revoked it is the record's `By` — the fact in the state, the
account in the trail, a year after either stopped being true.

**The derivation is a test, not a read.** `replayTo` folds a case's entries onto
an empty state and then hands the same entry list to `applyCommitEffects`, the
driver every real commit ran with the same effects list, so a reset no commit
recorded is derived rather than executed twice. It is deliberately not a second
interpreter: it takes no context, calls no hook and cannot fail, because DESIGN
§5.2 forbids revalidating old commits and §5.4's no-hooks rule gives the
reason — host code that answered differently last quarter is a different
answer, not a different case. `TestReplayLandsOnTheStoredSnapshot` runs it at
every revision of an eleven-commit case whose trail holds content, nested rows,
a decision and an attestation, and the derived state answers every address the
way the stored snapshot does. The tests beside it run §4.2's erasure and §5.1's
cascade out of the same entries, and one reads the trail through a definition
whose defaults moved, to show the fold lands on what the case holds now rather
than on the numbers a past revision printed.

Two limits keep the fold inside the package, and they are the reason nothing
like a `StateAt` is exported. The instance counter is not recoverable: §5.1
forbids reusing an ID, and a draft that added a row and dropped it before
submitting advanced the counter without any entry saying so, so a derived state
tracks the largest ID an entry *listed* — enough never to reissue an address a
host may still be holding, not enough to sit behind a case whose counter moved
in an unrecorded way. And the fold's base is the definition's defaults, which a
semantic reload may have moved, so a value nobody ever wrote is not in the trail
at all. Reading a derived state through a *replaced* definition is a further
question DESIGN §10 keeps open, the versioning policy that is milestone 4's and
not this slice's. So the exported read answers with entries that carry their own
payloads — true across a reload that would make a derived state lie — and §8
prices the fold it declines to run: 34 µs to derive the head of a thousand
commits, against 2.3 µs to read twenty of them.

---

## 6. Hook execution contract

DESIGN §7 fixes hook semantics; the image decides *where* they
intercept. Hooks are host code: the containment argument in §7 covers
definition content, and hooks sit outside it, bound by the read-only
`CaseContext` contract instead.

- **The seam is exported, and its keys are the definition's.** A host
  binds one normalizer per custom *type name* with
  `Runtime.RegisterValidate` and one provider per *field path* with
  `Runtime.RegisterOptions`. Registering again replaces what was bound
  rather than refusing, and the lookup goes through the registry on
  every call rather than through a table a handle copied: a deployment
  can fix its wiring without a new runtime, and every case it ever
  opened speaks through the function that arrived last. There is no
  error to return, because a key that matches no declaration is not a
  mistake the runtime can see — it holds no definition at that moment,
  and §3.8's binding is a key rather than a symbol. What a wrong key
  costs is the pair of absence rules below.
- **Two contexts, and they are different things.** Go's
  `context.Context` is cancellation and deadline, which §10 requires
  of anything that may do I/O — and for a hook that means an
  RPC-backed host rule. `CaseContext` is the case: identity
  (case, definition name, actor, stage, the committed revision and the
  copy's number), the effective role names, and by path a stored value,
  the decision governing a unit, and a signature field's attestation.
  A normalizer is handed, beside those, the value being assigned and
  the §5.1 address it is being assigned *at*; a provider is handed its
  own declared path, with no instance in it. The address matters
  because it is one vocabulary: the text a hook is handed, the text a
  later `FieldProblem` names, and the text `Update` takes back are the
  same string, so a host rule that considers the cell beside its own
  can say which cell it meant.
- **`CaseContext` is a snapshot, and neither a copy nor a lease.** It
  is pointers into state the operation already owns, because the store
  clones on the way out (§5.7) — so the bytes behind a context
  are written by nobody, before the call ends or after. A context kept
  past its call reads the state its own batch left, including a write
  that landed after the hook returned, and never a later batch's
  draft; DESIGN §7's "no hook result may be cached across a commit" is
  therefore a property of the shape rather than a rule to police, and
  reading a retained snapshot from another goroutine is safe because
  it is inert. The type has no exported constructor, and its zero value
  answers every read the way a withheld path does. The shape is not free,
  and what it costs is one small object per operation that pins a
  snapshot: naming the draft a batch writes means the working copy's own
  header is heap-allocated, whether or not any hook is registered,
  because an escape is decided by the compiler and a registry is not
  (§8). That allocation is the mechanism rather than the tax — a
  snapshot that pointed at stack would be the dangling kind.
- **`Validate` is bound by custom type** (DESIGN §7 lookup rule).
  `typeInfo.hook` carries it; the engine calls it for every value
  assigned to a field of that type and stores the *normalized* value
  for all later checks — the §5.3 re-evaluation, Submit's final
  validation, and the commit diff alike. A binding the host never
  registered is identity, per DESIGN §7's per-kind rule: the value the
  type checker accepted is the value the definition asked for. A binding
  that returns something of the wrong class, or a value the field's type
  cannot hold, is `HOOK_FAILURE` and abandons the batch — a host hook is
  code, and code that misbehaves is heard as a failure rather than
  absorbed as data.
- **`Options` is bound by field path.** Lowering attaches the binding to
  `options`-typed slots, and §5.7's walk is its only caller: the view
  asks when the subtree it is building needs a list (DESIGN §6.1), so
  `view` permission gates option sets exactly as it gates values, and a
  pruned node costs no call at all. There is no separate client
  `GetOptions` call, and E2 is what keeps `options` values out of
  conditions entirely.
- **The two bindings' absence rules disagree on purpose.** An
  unregistered `Validate` is identity, because the job it was hired for
  was to change the value and the unchanged value is what the type
  checker accepted. An unregistered `Options` provider is `HOOK_FAILURE`,
  naming the binding, because the job it was hired for was to answer and
  there is no value to fall back to: SYNTAX Appendix A's line that a
  missing hook is a runtime error and not silent success is the front
  end's promise about a dropdown, and inventing an empty list is how it
  breaks in production. The same distinction means a missing provider
  refuses the whole view rather than rendering the other fields beside a
  gap, and a selection that never reaches an `options` field asks nobody
  anything — a definition with a broken half still answers about the
  other.
- **`Options` is asked once per binding per call**, keyed by `hookID`, so
  a repeated field with two hundred rows costs one fetch and two hundred
  renderings of the list (§5.7). The key is the field path and not the
  instance because the *binding* is a declaration's — which is the same
  reason `CaseContext`, now that it ships, carries no row: a provider is
  never told which instance is being rendered, and SYNTAX §7's rule 11
  is what makes that a shape rather than a gap, because nothing in this
  language asks a question of one instance and not of the field.
  Pretending otherwise would be asking one question two hundred times
  and calling the answers independent.
- **Re-evaluation order:** apply edits → normalize per field
  (`Validate`) → re-evaluate the §5.3 closure → report validation
  errors against final values. Submit re-asks `Validate` of the values the
  submitted subtree holds, against the rules as they are today (DESIGN §7):
  the rules are host code, a host may have tightened them, and DESIGN §3's
  answer about old content is that it becomes *invalid* rather than
  incompatible. Two things follow from asking a question the engine already
  asked once. Only stored values are asked about — an unset cell is §5.4's
  completeness question and not a hook's, so a field is reported under one
  heading. And the stored value is a normalization's own output, so a hook
  that is not a fixed point on it is `HOOK_FAILURE` rather than a quiet
  rewrite of what the actor presented: §6's chain says the normalized value
  is what later checks see, and Submit is that later check.
- **Who the re-ask is allowed to reach.** Submit's walk is over every
  reached field of the submitted subtree, because the actor presenting it
  holds the grant for it. §5.7's is the same walk through the caller's
  visibility array, because a host rule is not entitled to run over
  content the caller could not render: neither its `FieldProblem` nor a
  `HOOK_FAILURE` naming the path may say something the mask withholds.
  The two are the same code with one argument different, which is the
  cheapest way to keep a view's report and a commit's from disagreeing.
- **And who may answer what a hook asks.** The same array gates a
  `CaseContext` read, so a host function cannot ask by path about a
  cell its caller was pruned from: the snapshot is pinned with the
  operation's own visibility fold where one was computed, and with the
  node's reachability — §5.7's rule and not a stricter one — where it
  was not. The two readings are required to agree, because a context
  that hid a value on a read and handed it over on a write hid
  nothing. A withheld path answers the way an absent one does, which is
  §7's property 7 with a host function as its subject: the channel this
  closes is wider than a rendered node's, because a read takes
  arbitrary text. §8 prices both branches: one array lookup where a view
  holds the fold, and a scan bounded by declaration breadth where an edit
  does not.
- **Failures:** hook execution failures, structural errors and forbidden
  edits reject the whole `Update` atomically — the batch is one act, so a
  refused one leaves the working copy exactly as it was, at the revision
  the caller already had, with no half-applied row. Ordinary
  validation errors persist with draft values for UI feedback (DESIGN
  §6.2; the error taxonomy is DESIGN §9); they are an operation *result*
  rather than a refusal, which is why §9's categories do not name one,
  and they arrive with the read side that would display them.
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
7. **Permission is enforced before anything is read.** The mask of the
   actor asking is folded over the whole image, or checked at the door of
   the operation, before a cell is loaded, a path is resolved past a
   denial, or host code is called. `View` is where that is load-bearing,
   because it is the one operation that calls host code out and writes
   nothing back (§5.7, §6): a node the caller may not see is pruned
   rather than rendered and filtered, so its options provider is never
   fetched and its `Validate` hook never runs. That closes the two
   channels a filtered output would leave open — work performed on
   withheld content, and a refusal whose message names it. The fold
   answers a hook's own questions the same way (§6): a `CaseContext`
   read is gated at the nodes the caller could have rendered, so a host
   function cannot ask by path about a cell its caller was pruned from,
   and a withheld value and an absent one are one answer. This is the
   wider of the two channels, because a read takes arbitrary text where
   a render takes only what the walk reached, and it is why the
   snapshot carries the caller's fold rather than the case's contents.
   The same rule decides what a *refusal* discloses: a path with no
   permission behind it is `ACCESS_DENIED`, decided by the mask alone
   and never by whether
   a row exists, and `NOT_FOUND` is answered only where the caller may
   see the node the walk stopped at — so an unseen instance of a
   collection in the caller's scope reads the same as a mistyped name,
   and probing the IDs of a withheld collection learns nothing except
   that it is withheld. Update, Submit and Transition put the permission
   test before the revision compare for the same reason (§5.4): a
   forbidden operation must not leak the case's revision traffic as a
   consolation. `Review` is the same rule over a fact neither of them
   touches (§5.9): the mask is tested at the resolved node before the
   revision is compared, and the two questions only it can be asked — is
   this path a *unit*, is a decision *standing* here — are answered after
   permission and, in the second case, as `INVALID_OPERATION` rather than
   `ACCESS_DENIED`, because a permission answer about a decision that is
   not there would tell the caller that somebody's is. What a decision
   *is* is disclosed the other way: state, author and comment are printed
   to any actor who may view the unit's content, including one who may not
   decide it, since a rejection no one can read is a wall and not an
   answer. `History` is the same fold over a past (§5.11), and it is this
   rule's second half: one `viewable`, at the roles the committed state
   carries and the stage the case sits at *now*, consulted per entry, so a
   trail cannot disclose what a form withholds and no rule about what somebody
   could have seen at revision 12 ever has to be written, maintained, or
   drifted from. Content is what gets filtered and the record of an act is not
   — a `Transition` commit has no content to show, and an audit whose
   revisions go missing silently is not an audit — and what a caller is owed
   about the gap is `Withheld`, a count that names no declaration.
   Reachability is the test for a row set and the grant at the node the test
   for everything else, the one place a trail asks harder than a view:
   `viewable` marks a container reachable to whoever may see a child of it,
   and a decision about the unit that owns it is content, not a path. The
   merge is held to the same rule in the opposite direction (§5.8): `Rebase`
   grants nothing, because the only state it writes is the caller's own draft,
   but every clash it reports is a statement about content, so a clash at a
   slot the actor may not view is counted and named by its form rather than by
   the row and field inside it. `Discard` asks no permission at all, and the
   reason is the same fold: the copy it destroys belongs to the actor asking
   (invariant 4), and the only numbers its refusals name are that copy's own
   working revision and the one the caller held — no case fact, no other
   actor's draft.

---

## 8. Cost model and benchmarks

The rows below are measured, with `go test -bench . -benchtime=1s`, on
an Apple M4 (Go 1.27.4, darwin/arm64). `benchmark_test.go` is the harness
for everything through §5.11 and `hook_benchmark_test.go` for §6's seam,
whose rows were printed at the fixed `-benchtime=200000x` an isolated
harness needs rather than the timed default, and are the median of five
counts. Every pair compared below was measured in one process, so the
pairs differ by the code and not by the clock. A number here without a
benchmark in either file is a target, not a result. The
reference corpus is the SYNTAX §7 example (20 slots) plus
§8's generated wide case: one form, 40 fields, one 8-field `multiple`
group, 6 roles, 5 stages — 51 slots. Its roles carry `view, edit all`
rather than `view` alone, because a fixture an operation can put content
in is the only kind a read can be measured against: an empty frame makes
a `View` traversal cheaper than the profile this section describes, and
an `Update` on it impossible.

| Benchmark | Measures | Result |
| --- | --- | --- |
| `BenchmarkCondEval` | one `tagCmp` guard | 2.1 ns, 0 allocs |
| `BenchmarkPermResolve` | 51 slots, one role | 403 ns, 0 allocs |
| `BenchmarkResolvePath` | caller text → a cell | 70 ns, 0 allocs |
| `BenchmarkTransition` | one committed stage move | 190 ns, 2 allocs |
| `BenchmarkTransitionGuard` | one refused stage move | 187 ns, 5 allocs |
| `BenchmarkViewWide/all` | whole visible case, wide | 7.9 µs, 230 allocs |
| `BenchmarkViewWide/one` | one field by path, wide | 1.3 µs, 21 allocs |
| `BenchmarkLoweringOnly` | schema → image, §7 | 12.6 µs, 248 allocs |
| `BenchmarkLoweringWideOnly` | schema → image, wide | 30.3 µs, 295 allocs |
| `BenchmarkLowerSpecExample` | source → image, §7 | 41.7 µs, 1074 allocs |
| `BenchmarkLoweringWide` | source → image, wide | 61.0 µs, 1214 allocs |
| `BenchmarkRebaseClean` | an unseen commit and its merge | 3.3 µs, 61 allocs |
| `BenchmarkRebaseSynced` | `Rebase` with nothing to merge | 288 ns, 6 allocs |
| `BenchmarkRebaseRows` | the merge over churned rows | 8.6 µs, 186 allocs |
| `BenchmarkDiscard/draft` | a batch, then the drop of it | 920 ns, 20 allocs |
| `BenchmarkDiscard/absent` | `Discard(0)` beside no copy | 33 ns, 0 allocs |
| `BenchmarkTransitionRequires` | one gated move in three | 370 ns, 3 allocs |
| `BenchmarkReview` | one committed decision | 340 ns, 4 allocs |
| `BenchmarkReviewNoOp` | a decision already standing | 79 ns, 1 alloc |
| `BenchmarkReviewRefused` | a decision not granted | 140 ns, 4 allocs |
| `BenchmarkSign` | one committed attestation | 480 ns, 8 allocs |
| `BenchmarkSignNoOp` | an attestation already standing | 225 ns, 5 allocs |
| `BenchmarkSignRefused` | an attestation not granted | 208 ns, 5 allocs |
| `BenchmarkSigReset/standing` | one entry, signature live | 193 ns, 4 allocs |
| `BenchmarkSigReset/empty` | one entry, none live | 98 ns, 1 alloc |
| `BenchmarkHistory/page` | 20 of a 1,000-commit trail | 2.3 µs, 28 allocs |
| `BenchmarkHistory/whole` | all 1,000, 3 entries each | 86.9 µs, 1,015 allocs |
| `BenchmarkReplayTo/head` | the fold to the trail's head | 34.3 µs, 7 allocs |
| `BenchmarkReplayTo/first` | the fold to revision 1 | 408 ns, 7 allocs |
| `BenchmarkHookSeam/update/bound` | writes under a rule | 3.38 µs, 74 allocs |
| `BenchmarkHookSeam/view/bound` | a view re-asked | 10.52 µs, 260 allocs |
| `BenchmarkCaseContextReads/value_fold` | one masked read | 54.6 ns, 0 allocs |
| `BenchmarkCaseContextReads/value_range` | by scan | 57.2 ns, 0 allocs |

The eight rows above §5.11's are §5.9's and §5.10's, and the five that go
through an operation were measured in one process, twice each, with
`BenchmarkReview` re-run beside them so the comparison is worth making. The
re-run printed 338 to 344 ns against the 340 above, 81 against 79 and 140
against 140, so the review rows are the same clock and not a borrowed one.
The ordering does not depend on that clock at all: a refusal is the cheapest
call of either kind, a commit the dearest, and a commit and its no-op differ
by the store's work and nothing else — `BenchmarkSign` minus
`BenchmarkSignNoOp` is 255 ns against `BenchmarkReview` minus
`BenchmarkReviewNoOp` at 257, which is the claim that an attestation is a
commit of the same size as a decision, stated as two subtractions.

The difference between the two operations is what `Sign` asks first. A
signature is refused unless the form is complete and decided, so the
attestation carries §5.4's completeness walk and §5.9's pending-unit scan
before it looks at the table — 140 ns over `BenchmarkReview` on this
corpus, and the profile says the walk rather than the scan is where it
goes: `pathsOf` and `hostsOf` together are half the allocations of the call,
while the table itself is one index and one copy. The gate is asked before
the dedup, so `BenchmarkSignNoOp` is not `BenchmarkReviewNoOp`'s
near-nothing: a repeated attestation pays the whole readiness question and
then discovers it is already true, which is the price of a guard that reads
the case rather than the request. `BenchmarkSigReset`'s pair is the effect
alone, measured against itself: 193 ns and 4 allocations where an
attestation stands, against 98 ns and 1 where the table is empty, and the
allocation left in the empty row is the fixture's own table rather than work
the effect did — which is the property §5.6's `reach`/`open` split exists to
buy. The gap between the rows is charged per commit and not per invalidated
field, so the rebase that moves four fields of one form erases that form's
signature once.

Three things follow. A condition is two memory reads and a compare, so
§3.4's "no type dispatch inside a running condition" is a cost claim and
not just a design claim. A permission answer is one array index, an
overlay binary search and an AND-NOT, and it allocates nothing: 8 ns per
node is below the cost of deciding what to show for it. And a whole
definition lowers in tens of microseconds, with the front end accounting
for 3.3× the bill — which is §9's argument for rebuilding images rather
than serializing them, stated in numbers.

A path request joins those three at the cheap end: 70 ns to walk four
segments, intern nothing, and hand back a slot, a row and a position —
the §3.9 seam is not a tax on every read.

The fanout's cost is stated as a ratio rather than a row, because what
it claims is about proportions: in one run, a field write on the 20-slot
corpus, a write crossing both edges that corpus has, and every
declaration of it marked at once came out at 1 : 1.4 : 2.2, and the same
single write against the 51-slot corpus cost 1.9× the small one. Three
allocations in every case — the visited set, the queue, the paths — and
none per edge. That is §5.3's bound claim in one sentence: the numbers
move with the definition, not with the batch.
(`BenchmarkUpdateFanout`.)

Charged to a whole batch, the two arrays are paid whatever the answer
turns out to be — `newFanout` runs before the first edit does — and the
paths are allocated on the way out. A same-process A/B that skipped the
closure and its render, leaving the arrays in place, came out two
allocations and about a tenth of the time cheaper, which is the honest
reading of what the walk costs a request: the render, plus the queue
drain, not the reservation. `BenchmarkUpdate`'s absolute row waits for a
clean run, for the reason stated below about absolute rows in general.

The commit path is ratios too, and it has two because §5.4 has two answers.
A submit that writes nothing — every check passes, the diff comes out empty —
reads the copy, walks §5.2's completeness and §6's `Validate` over the form's
subtree and compares it, for about 1.3× what an accepted three-write `Update`
on the same corpus costs and one allocation more: the candidate's clone is
the only structure a submit adds to what an edit already pays for. A submit
that did commit, measured as the edit followed by the submit of what it
changed, came to 2.8× the edit alone, and that pair splits about 3 : 1
between deciding and writing. So the store's three consistent writes —
including the copy of the committed snapshot §5.1's materialized state makes
a commit pay — are the cheap part of a commit, and the walks are the bill:
which is what §5.2's index-based answers and §5.6's direct indexing are for.
(`BenchmarkSubmit`, `BenchmarkSubmitNoOp`.)

The view is the expensive read, and its two sub-rows are the comparison
that says where the expense is. Whole-case at 7.9 µs against one field at
1.3 µs: 6.6 µs and 209 of the 230 allocations is the *walk* — the 51
slots whose mask was just folded, rendered as 78 nodes because four
instance rows multiply the group's eight members, a node struct and a
rendered path apiece, and the `validate` subtree pass beside it: about
85 ns and under three allocations per node. What the 1.3 µs floor is made
of is the other half of the answer, and it is not small: `visible()` is
the same 390 ns fold `BenchmarkPermResolve` prints, and the snapshot
clone `viewed` performs for a read that changes nothing is most of the
rest — a one-field view allocates 10.2 KB, against a submit's 10.7 KB for
two whole walks and three consistent writes.

So a client that renders one field at a time pays the per-call costs once
per field, and the arithmetic is why §6.1's contract asks for *several*
paths and one snapshot: the fold and the clone are charged per call, a
resolution per path it is handed, and the nodes per node — so a subtree
selection is the cheap shape of the same question, and a form refreshed a
field at a time pays for the same content twice over. That is also why
`viewed` refuses to create a working copy (§5.7): a read that allocated
per-user state would add a cost of a different kind to this row, one
that grows with the audience rather than with the form.

Charged against the other operations, a whole-case view is about 9× an
accepted three-write `Update` and about 3.3× a `Submit` — of course it
is, it builds an object per declaration while an edit writes one cell —
and the useful ratio is the inverse of §5.4's: a submit's walks are what
a view's report is *for*, so the engine pays to render what the commit
paid to decide. A walk up the slot table to find a node's form, which an
earlier draft of §5.7 carried for `CanSubmit`, was deleted rather than
measured: §3.5's fold is total, so the node answers its own question
exactly as its form would, and paying a parent chain per node to ask it
twice is the kind of cost a benchmark only finds after the fact.

Absolute rows here are only as good as the machine that printed them: a
re-run under background load read some five times *every* figure in the
table, the untouched ones too. So where the point is a proportion — the
front end against lowering, the fanout against itself — the section says
so, and a regression check should compare against a baseline measured in
the same process rather than against this table.

The transition path is the cheapest commit in the engine, and it is cheap
for the reason §5.5 says it should be. Measured in one process with the
rows above, a stage move came out between about a fifth and a quarter of
the accepted three-write `Update`, and between an eleventh and a fifteenth
of the update-then-submit pair, at two allocations; the ends of those
spreads were consecutive runs of the same benchmark, on the machine §8's
caveat is written for. It pays for no completeness walk over a candidate,
no `Validate` re-run, no diff and no graft — the snapshot moves beside a
new stage rather than being copied — so what is left is the role fold, one
mask test, one revision compare and the store's three writes.
(`BenchmarkTransition`.)

The row after `BenchmarkTransition` is the only one here that prices a
destination that *asks* a `require`, and it is not comparable with the rows
beside it: one commit per iteration, cycling `person_review`, which declares
`require application`, then a reject to `correction` and an accept back to
`draft`, neither of which asks, over a case holding submitted content and a
standing approval — where `BenchmarkTransition` moves an empty case between
two stages that ask nothing. So 370 ns is one gated move amortized over two
ungated ones, which makes it dearer than the ungated move's number and
cheaper than the gated move's, and it is left that way rather than divided up
on assumptions about what each third cost. What the row is here for is §5.5's
claim about the conjunct §5.9 added. The gated move runs the completeness walk
over the required form's subtree and `Validate` beside it, and the approval
scan rides the same span: three runs of the loop against three of a scratch
copy with the scan's result dropped came out 369 ns against 363 ns on the
means, 1.6% apart where consecutive runs of one copy were 3% apart, at the
same three allocations either way. So the walk's cost is §5.4's and the scan
adds nothing that separates from it, which is what "read `verif` off the
table the pass is already walking" is supposed to buy — and the reason the
conjunct is reported here as two numbers and their spread instead of as a
fifth row. (`BenchmarkTransitionRequires`.)

The same call stopped at an edge's condition costs the same hundred and
eighty nanoseconds and *more* objects than the one that committed: five
against two, in fewer bytes, because three of them are the refusal's
message. Read that beside the 3 : 1 deciding-to-writing split a submit
pays, and the two operations are opposites for the same reason — a submit
is walks around a cheap write, a transition is a write around nothing.
Either way the engine's cost is in its passes and not in its store, which
is what §5.2's indexed answers and §5.6's direct indexing are for.
(`BenchmarkTransitionGuard`, driven by the SYNTAX §7 example's
reject-to-`correction` and accept-to-`draft` loop so that each call is one
commit and neither destination asks a `require`.)

A decision is priced beside the stage move it most resembles: 1.8× a
`Transition` at four allocations against two — the table copy, the one-entry
diff, the history record — and the reason it is not *cheaper* than a stage
move, when it walks nothing, is that it writes a slice a transition does
not. Already standing, the same call is 0.42× a transition at one allocation:
the fold, the resolution, the mask test, the unit test, the revision compare,
and a return with no message in it, which is §7's property 7 priced — the
door is cheaper than the commit, and nothing about the case is read to answer
at it. A granted decision is 2.4× a refused one, and nearly all of the
difference is the commit's three writes rather than any check.

Beside the operation that decides content, an accepted `Submit` on the same
corpus is 6.8× a `Review`: a review runs neither of §5.4's walks, grafts no
state and diffs nothing. §5.9's claim that the operation is therefore this
package's cheapest commit is only true of the *store*: `Review` is dearer
than `Discard`'s no-copy path by an order of magnitude, and that is the
difference between committing and declining to. (`BenchmarkReview`,
`BenchmarkReviewNoOp`, `BenchmarkReviewRefused`, against `BenchmarkSubmit`
and `BenchmarkTransition`.)

Two claims about what §5.9 adds to operations it does not belong to, both
measured as differences against a scratch copy of this package with the code
removed rather than as absolute rows, because a cost of one percent is not
visible in a table of absolutes. The reset a commit applies (§5.6's first
consumer) costs a scan of that commit's own entries when nothing stands
approved, and that scan does not show up: three runs of `BenchmarkSubmit`
against three of a scratch copy with the call removed came out 2384 ns
against 2400 ns on the means — the copy *without* the reset a hair dearer,
a gap inside the 2% that consecutive runs of one copy showed each other —
at the same 42 allocations and within 70 bytes of each other either way.
That is what O(diff) with a constant of one array read per entry is
supposed to look like: §7's `application` is a verifiable form, so every
entry this benchmark writes does get looked at, and looking at it costs
nothing a commit was not already paying. The allocations are the part of
the comparison that cannot be noise, and they say the second half of
§5.9's claim — a commit whose diff decides nothing takes no table copy at
all. The second effect was measured the same way, against a scratch copy
that runs one effect instead of two: `Submit` came out 2,281 ns on the means
of three runs without it and 2,329 ns with it, on the same corpus, at the
same 42 allocations and within 25 bytes either way. That is the range §4.2's
invalidation adds to a content commit: one read of the entry slot's owning
form, and then one read of a nil table per signature field that form
declares — two on §7's `application`. The two sides' own runs spread 31 and
48 ns, so the honest reading is a cost below the noise of one run and above
the noise of three, which is what O(diff) with a constant of two array reads
per entry should look like. The allocations are the half of the comparison
that cannot be noise, and they say what §5.6's split promises: a commit that
erases nothing copies nothing, whichever effect found the entry.

The two fields a node now carries cost about a nanosecond and a half
apiece: the whole-case view measured 8,578 ns with `Reviewable` and `Review`
left zero against 8,705 ns with both computed — a re-run of the row above,
one process per copy, which is what this section's caveat about absolute
rows asks for of a comparison like this — across 78 nodes. That is 1.5%
against the 1.6% consecutive runs of the computed copy differed from each
other by, at the same 230 allocations and the same 35,267 bytes either way,
and a cost that shows in the mean and not in the allocations or the bytes is
the signature of arithmetic added to a walk that was already holding the
answer. The flag is a slot-field compare, and for a node whose `verif` is
its own it is a second compare against the instance path, and only then the
same `effective` lookup `CanEdit` and `CanSubmit` each pay: one array read,
one overlay search, one AND-NOT and a bit test. The info is one read of the
decision table, and the name it carries is two more — the governing slot's
stored `path` and then the interned string, with no walk to the root because
a declaration's path is stored and not rebuilt (§3.1). A view that had to
*derive* whether a decision still stands, by searching the history for a
content commit newer than the decision's own — the alternative §5.9 declines
— would have no such small number to point at.

§5.10's pair is not measurable at all, and the reason is the same arithmetic
one step further along: the whole-case view on the wide corpus came out at
8,882 to 9,079 ns with `Signable` and `Signature` computed and 8,896 to
9,104 ns with both left zero, three runs each side, at identical 230
allocations and 40,259 bytes. Means that interleave are what one slot-field
compare per node should look like — and on that corpus every slot answers
`sig == 0`, so the flag never reaches the mask and the info never reaches
the table. Where a definition *does* declare signature fields the flag is
the same `effective` lookup `CanEdit` and `CanSubmit` each already pay, and
the info is one index into a table that is nil until somebody signs, so an
unsigned case is answered without a memory read past the slot.

Synchronization is priced as a difference, because the loop that measures
it contains the commit the merge is reconciling. `BenchmarkRebaseClean`
runs one unseen `Update` and `Submit` and then the `Rebase`; the same pair
alone is `BenchmarkSubmit`'s 2.3 µs and 42 allocations, measured in the
same process, so the merge is about a microsecond and nineteen
allocations: two clones of the §7 case's state — the copy as it is read,
the draft the merge writes into — beside the walk over a window of one
commit. `BenchmarkRebaseSynced` is that same call with no window at all,
and its 288 ns is what §5.8's "not a merge and not a write" costs: the
look-up, the fold, and the one clone `draft` performs before anybody asks
whether the copy has moved. A synchronized rebase that skipped that clone
would be cheaper than the row here, and the row is the floor the other two
synchronization numbers sit on.

`BenchmarkRebaseRows` is the merge over a case whose rows churn — a
shareholder issued and one removed every iteration — and it is the row that
states §5.8's bound rather than the one that reads well. Each iteration
commits, so a second of timing leaves the case with some hundred thousand
revisions behind it and the call still costs 8.6 µs: the walk stops inside
the window's own keys,
because a list is answered by the first entry at or below the base, and a
value in a row the base did not have is answered by that list instead of by
the ledger. The same merge with the two passes in the other order — cells
asked of history before membership is known — measured 75 to 90 µs and
3,796 allocations in a scratch copy of the package run at 1,000 iterations,
a figure that grows with the case's past rather than with its changes. That
is the failure mode the ordering exists to avoid, and it is why §5.8 states
the walk as lists first.

`BenchmarkDiscard`'s two sub-rows are the guard, and nothing but the guard.
The drop of a draft that holds work, in the same loop as the batch that made
it, costs 920 ns against that batch's 864; the same call beside no copy costs
33 ns and allocates nothing, which is the read of a case record and a
comparison. So §6.5's cheaper exit is free in the engine and its price in a
host is the work it throws away — which is the claim the row is here to make
legible rather than impressive.

§5.11's two pairs are the same claim asked of a case with a past, and each row
of a pair is the reason the other is cheap. A page of twenty records of a
thousand-commit trail costs 2.3 µs and that trail's whole thousand 86.9 µs:
87 ns a record at both ends, with the mask fold and the result's own assembly
paid once per call however far back the cursor starts. Nothing searched or
copied the trail to answer either, which is what `window` being index arithmetic
and an entry rendering from its own record buys — and it is why the pair's
ratio, 37× for 50× the records, is the section's cost claim rather than the
absolute row. The other pair is the fold the exported read declines: 34.3 µs to
derive the head against 408 ns to derive revision 1, and the 84× between them is
the trail itself. Reading history and rebuilding a past state are the same
entries; what differs is that one answers with what an entry recorded and the
other applies every one of them, which is why §5.11's exported word is a trail
and the derivation's price is paid by the test that checks §5.1's claim. The
four rows were measured in one process with `BenchmarkSubmit`, which re-ran at
2,291 and 2,279 ns — the 2.3 µs and 42 allocations the synchronization
paragraph takes as its baseline — so these are the same clock and not a
borrowed one. What that call prints on this branch, and which two
allocations the exported hook seam added to it, is §8's paragraph on the
seam.

The seam's rows come from one process that also re-ran `BenchmarkUpdate`,
`BenchmarkSubmit` and `BenchmarkViewWide/all`, so every comparison drawn here is
same-clock: the fixed count is the price of putting a whole-case view, a batch,
and the isolated reads on one machine at one hour.

The seam is priced against an empty registry first, because that is the shape
most deployments run. `BenchmarkHookSeam/update/no_binding` is five writes of a
custom decimal type with nothing registered against it, at 2.98 µs and 54
allocations, and the same corpus read whole costs 9.42 µs and 230. That unbound
`View` is `BenchmarkViewWide/all`'s own corpus but for a type declared instead
of a built-in repeated, and the re-run of that row beside it printed 9.05 µs
against 9.42, the same 230 allocations, and 40,258 bytes against 40,257: naming
a type costs a read four percent and no memory, which is §3.8's point that a
binding is a key rather than work. A declaration binds nothing by itself. What a
binding costs is the next two rows.

With an identity rule bound the batch is 3.38 µs and 74 allocations — 80 ns and
four objects per call, against a host function whose whole body is `return v,
nil`. The objects are the contract's rather than the host's, and they are one
rule of it: every one of the twenty a bound batch adds over an unbound one is
`img.store` reached from `settle`, where checking a decimal against its declared
precision builds a big integer to test — `decimalFits` is the frame, three and a
half objects a visit, and a bound call is settled twice because §6 treats what a
host returns like what a caller sent. The unbound row pays three and a half of
the same objects a write for the values it settled anyway, and a third of either
row's objects are the store's clone of the state — neither of which the seam
adds or removes. The row is a floor, not a sample of host work.

Adding one masked read to the same rule costs 292 ns over the batch, 58 ns a
read, and no allocations at all: the snapshot is a struct of pointers the
operation already held, and reading it indexes them.

A whole-case `View` asks ten times rather than five, because §5.4's second
reading is asked of every value and a field holding its declared default holds a
value, so 9.42 µs and 230 allocations become 10.52 and 260: 111 ns and three
objects per re-asked cell, dearer per cell than a write's 80 ns because the walk
loads the stored payload into a `Value` before asking and settles the answer
after. One masked read per call adds 525 ns there, the fold's one array lookup
ten times over, at no allocations.

`BenchmarkCaseContextReads` prices the reads apart from any operation, on
snapshots pinned by real batches, and each pair is the two branches of §6's mask
rule asked of one path. A visible leaf is 54.6 ns where the caller's fold
answers it and 57.2 where the preorder scan does, at zero allocations either
way: on a corpus that grants this role everything the scan stops at the node
itself, so the two readings are the same work, which is what §6 requires of
them. They part where a subtree is withheld, which is the case the rule exists
for. A leaf of a form this actor may not render is 30.6 ns against 34.2 — a
leaf's own span is one node — and a container of it is 24.8 against 178.1,
forty-one `effective` lookups that must every one answer no before the walk may
say so. That 153 ns is the widest the mask can be made to cost a read: four
nanoseconds a node, against the eight `BenchmarkPermResolve` spends per slot to
fold a whole definition, and a function of declaration breadth rather than of
anything the case holds (§7's property 3). Absence is cheaper than either
branch: a path no declaration is reached by fails resolution before the mask is
asked, at 44.8 ns, and a verdict read that finds no unit governing its field is
31.8. And `Roles` is the one read that allocates, 9.7 ns and one slice, because
the list belongs to the host after the call. These rows may inline what a hook
cannot, since a function reached through the registry is an indirect call; that
is why each of them is also priced inside its operation above.

Charged against the previous commit — a worktree of it, one machine, one day,
one fixed count — the seam costs an operation that binds nothing one small
object. Allocation counts are exact; times are medians of repeated counts in two
processes, so what this paragraph claims is objects. `BenchmarkUpdate` went from
means of 865 to 873 ns and 18 allocations to 891 to 899 and 19, and
`BenchmarkSubmit` from 2,298 to 2,328 and 42 allocations to 2,339 to 2,346 and
44 — the 42 the history paragraph above takes as its baseline.
`BenchmarkViewWide` did not move in memory at all, 230 allocations and 40,260
bytes on either side of the change, and its times came out inside each other's
spread at 8,819 to 8,905 ns against 8,872 to 8,963. `Rebase` and `Discard` moved
by exactly their own arithmetic, 61 allocations to 63 and 20 to 21, because the
loop each one times contains a batch: `BenchmarkRebaseClean` is an `Update`, a
`Submit` and a merge, and `BenchmarkDiscard/draft` an `Update` and the drop of
it. The extra object is the operation's own working copy: `pinned` hands the
snapshot the draft by address, so the compiler moves the batch's `workingState`
header to the heap — `go build -gcflags=-m` reports `moved to heap: w` at the
two sites and did not report it at the previous commit — and that is the
mechanism by which §6's rule that a context kept past its call reads the state
its own batch left is *sound* rather than dangling. It is paid whether or not a
hook is registered, because an escape is a compile-time fact and the registry is
a runtime one, and it stops being the seam's business when milestone 4's read
transaction hands back a snapshot that cannot be written at all (DESIGN §8). One
object in eighteen, two in forty-two, and no byte of the whole-case read: what a
bound batch pays the seam is the two percent of wall time it adds, and that is
the registry's lookups and the snapshot's construction rather than this header.

Nor is any "× faster than the compiled tree" ratio measured: that would
mean an interpreted-tree harness, and building one to prove a point the
absolute numbers already make is a cost of its own. The multiples this
section used to promise are dropped;
the measured rows above are the facts, and a regression is now something
a re-run shows.

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
- **No hot reload of images.** Replacement is an explicit `Load` plus
  `Runtime.Replace` (DESIGN §3), and the pair is the whole mechanism:
  nothing watches a file, and `Replace` itself touches no case data — it
  swaps what an ID resolves to. The compatibility question is asked where
  a snapshot is actually read, which is a case operation: §4's drift
  check refuses a definition whose ordinal space moved rather than
  reinterpreting the data into nonsense, and a case nobody opens again is
  never asked.
- **No synchronization nobody asked for, and no merge that decides.**
  Committed change never rewrites a draft by itself, and `Submit` still
  refuses a copy that has fallen behind rather than reconciling it first —
  DESIGN §6.5's "no implicit merge occurs during Submit" is kept literally
  (§5.8). What the merge that does exist will not do is choose: it takes a
  side nobody moved, and two row lists both sides moved are refused and not
  joined, because ordering them is a policy no document states. That half of
  §6.5's deferral stands.
- **No abstracted commit ladder.** `Update`, `Submit` and `Transition`
  share a shape — look the case up, resolve what was named, ask
  permission, ask the numbers, ask the content, then save — but the
  interleaving of those questions is the per-operation decision §5.4 and
  §5.5 exist to state, and §5.5's conditional rebase is what a shared step
  costs when one operation cannot rely on the reason the other had it
  pass. §5.8's two operations join the shape without joining the commits:
  they look the case up, ask the numbers, read the content, and then save
  the copy or delete it, with nothing resolved and no permission asked
  beyond the mask that decides what may be named. The trigger this item set
  has now fired: `Sign` is the seventh instance, and the shape held without
  being abstracted — it reused §5.4's walk, §5.5's scan and §5.9's guard
  order, and joined the store's `commit` as another caller rather than as a
  new ladder. Seven instances state the same six questions three different
  ways, because the order *is* the semantics: `Submit` asks the copy's
  number, `Transition` asks the case's and no copy's, and `Review` and
  `Sign` ask one number because they present no draft to descend from. A
  shared skeleton would have to be parameterized by exactly the thing it
  would factor out, which is §2.3's argument against a general VM stated at
  the operation seam.
- **One `Affected` policy is a milestone 5–6 decision.** DESIGN §6.4
  promises an affected set for every committing operation; `Submit`,
  `Review` and `Sign` ship without one and `Transition` with the §5.5
  definition walk, because notification is the only consumer of such a
  list and it is `OnCommit`'s shape that decides what the lists must
  contain. The four are decided together, not one operation at a time —
  and the two acts ask the question that makes deferral the honest answer
  rather than a gap: a decision and an attestation each change no
  content, so an affected set for either could only name a subtree to say
  that a verdict landed, which is a notification payload and not a §5.3
  walk.
- **A hook result that carries a code is still undecided.** The exported
  signature answers a value with `(Value, error)`, and the one error word
  means two things depending on the call: against a value arriving it
  abandons the batch with `HOOK_FAILURE`, and against a stored value at
  commit or in a view it becomes a `FieldProblem`. DESIGN §7's tri-state
  — `Valid | Invalid | Failure` — would give a host a way to say
  "invalid, and keep it", which is a decision about what an accepted
  `Update` reports and how a draft may hold a value a rule refused.
  `CaseContext` settled the *input* half of that sketch; the output half
  stays where the design left it, and `INVALID_VALUE` is still the only
  field code the engine owns, because inventing more would be this
  package guessing at what a host meant.
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
its tests and the two language documents. So are 2 (*pure case semantics*)
and 3 (*views and hooks*), and the item that closed the pair is §6's seam.
Milestone 4 is the one in progress: its contract is what the in-memory
store has honoured since §4, and its storage implementation is not
written. The image of §3, the lowering of §4, the
`Load` seam, the frame and instance rows of §5.1, §5.2's presence and
requiredness and §3.9's resolution seam are in place and tested, and so
is the exported half that reads them: `Runtime` and `Case` (DESIGN §6),
the registry and the store seam, §5.1's four working edits as one atomic
`Update` (§6.2), §5.3's fanout behind `ChangeResult.Affected`, and §5.4's
`Submit` with §5.6's forward diff and the store's history behind it, and
§5.5's transition checks behind `Transition` — the second operation that
commits, and the first whose affected set is about masks rather than values.
§5.7's view walk is behind `View`, milestone 3's first item: the mask folded
once per call, the draft read without a copy being made, the selections merged
on address, and `Options` fetched and `Validate` re-asked through that mask.
Milestone 5 is whole now, and the noun that finished it was its last.
`Review` came early and for a reason of its own (§5.9): the SYNTAX §7
walkthrough reaches `company_review` *after* an approval, so the conformance
trace this section is graded against needed a reviewer before it needed a
signer, and a transition that asks for an approval nobody can make is a gate
with no key. `Sign` is the rest of them (§5.10), the fourth operation that
commits and the second whose commit says nothing about data, and it carries
the two halves that were still owed: §5.5's third conjunct, so a
destination's `require` now asks all three of the questions its line states,
and §5.6's `sigEntry` invalidation, so no attestation outlives the content it
bound. Beside `Reviewable` and `Review` the view now reports `Signable` and
`Signature`, which tell a client where a hand may attest and which hand did.
Appendix A's trace runs the attestation as well as the approval, in a second
trace beside the first, because the shipped §7 example marks no signature
`required` and the claim needs both shapes to be true rather than one.

§5.11 is that noun: `History` exported, and the fold behind it. The two
verdict entries §5.6 argued for one commit at a time now have their reader, in
the two shapes a standing verdict is reported in, and DESIGN §5.2's second
version of the state — the read model that *derives* what stood at revision
*i* — is a claim the package can make to itself and check: `replayTo` writes
each entry's payload onto an empty state, hands the same list to the driver
every real commit ran, and `TestReplayLandsOnTheStoredSnapshot` compares the
result with the stored snapshot at every revision of a case whose trail holds
content, nested rows, a decision and an attestation. The claim §5.1 had been
making since it was written — forward diffs, replayed — is now the thing a test
fails on. What is *not* exported is the derived state, and the two reasons are
§5.11's: an instance counter no entry recorded, and a defaults baseline a
semantic reload may have moved, beside the definition-versioning policy
DESIGN §10 leaves open for milestone 4. `View`'s mask is the read's mask, asked
now, and §7's property 7 closes with the test that says a page of history
cannot disclose what a form withholds.

§5.8's merge remains the trail's other consumer, and the two do not overlap: a
merge reads a window the caller's draft is behind and reconstructs nothing,
while the read here answers what a case did and the fold answers what it held.

DESIGN §10's milestone 6 has landed in part, and early: `Rebase` and
`Discard` are here (§5.8) — the third version read out of the store's own
history rather than replayed, a merge that takes an unchanged side and
refuses to choose between two moved ones, nothing saved when it cannot, and
a refusal that counts the clashes it may not address. Its other half, the
observers, waits for `OnCommit`. And §6's hook contract is exported now,
which is what closes milestone 2's validation item and milestone 3's hook
item in one piece: `RegisterValidate` keyed by
a custom type's name, `RegisterOptions` keyed by a field path, and the
read-only `CaseContext` DESIGN §7 describes arriving as the argument both
signatures carry. A declared `Validate` normalization runs three times —
against a value as it arrives, against the stored one at commit, and
against what a view renders — and it is now one function asked about one
cell at one address each time, which is what makes the third asking a
check rather than a second opinion.

Contract at the seam. Three halves are exported: the definition seam this
handoff was written for, the case operations milestone 2 has grown beside
it, and the hook seam §6 a host wires into a runtime. `ctx` stands for
`context.Context`, and the operations that read the store take it and can
refuse.

```go
// Source in, an immutable definition out.
func Load(id string, src io.Reader) (*Definition, error)
func (d *Definition) ID() string
func (d *Definition) Warnings() []Problem

type Problem struct { Line, Col int; Code Code; Context, Msg string }
type Code string

type Error struct{ ID string; Problems []Problem; Defects []string }
func (e *Error) Mismatch() bool

// The registry, the actor, and one handle per case.
func NewRuntime() *Runtime
func (r *Runtime) Define(id string, src io.Reader) (*Definition, error)
func (r *Runtime) Register(d *Definition) error
func (r *Runtime) Replace(d *Definition) error
func (r *Runtime) Definition(id string) (*Definition, bool)
func (r *Runtime) Create(ctx, defID string, actor Actor) (Case, error)
func (r *Runtime) Open(ctx, id CaseID, actor Actor) (Case, error)

type Actor struct{ User UserID; PrimaryRole string }
type UserID string
type CaseID uint64

func (c Case) ID() CaseID
func (c Case) Actor() Actor
func (c Case) Definition() (*Definition, bool)
func (c Case) Stage(ctx) (string, error)
func (c Case) Revision(ctx) (uint64, error)
func (c Case) Update(ctx, base uint64, edits ...Edit) (ChangeResult, error)
func (c Case) Submit(ctx, form string, working uint64) (CommitResult, error)
func (c Case) Transition(ctx, dest string,
    observed uint64) (TransitionResult, error)

// A decision about what the case holds, not about what a draft would put
// in it (§5.9): no working copy is named, and none moves.
func (c Case) Review(ctx, target string, act ReviewAction,
    observed uint64, comment string) (ReviewResult, error)

// An attestation about the same content (§5.10): `observed` is the
// committed revision the signer was shown, and `evidence` the host's own
// reference to what its ceremony produced — kept as given, interpreted as
// nothing. The result carries a `Missing` list because a refusal here is
// about the form's readiness, which `Review` never asks.
func (c Case) Sign(ctx, field string, observed uint64,
    evidence string) (SignResult, error)
type SignResult struct{ Revision uint64; Stage string; Missing []string }

// Whose hand stands behind a signature field, as the view reports it. `At` is
// the revision the attestation's own commit produced, so the content it bound
// is the content one commit earlier (§5.10); the zero value is the whole
// answer for a field nobody stands behind, and it is the same answer whether
// the hand never came or came and was erased by content.
type SignatureInfo struct {
    By       UserID
    At       uint64
    Evidence string
}

// Edits, values, and what an accepted batch says.
type Edit struct{ /* unexported */ }  // Set · Empty · Add · Remove
type Value struct{ /* unexported */ } // Text · Int · Decimal · Bool · Date
                                      // · DateTime · Tag · Binary · User
type BinaryRef []byte
type ChangeResult struct{ Working uint64; Created, Affected []string }

// What a submit reached, and — when it committed nothing — what stopped it.
type CommitResult struct {
    Revision uint64; Stage string; Missing []string; Invalid []FieldProblem
}
type FieldProblem struct { Path, Code, Msg string }

// What a stage move reached, and — when the destination was not ready — what
// it still wants. A transition always commits, so there is no "revision it
// did not move" to report.
type TransitionResult struct {
    Revision uint64; Stage string; Affected, Missing []string
}

// What a decision reached: the case's new number and its stage. A review
// asks nothing about content, so there is no `Missing` list here — the
// approvals a destination still wants are `Transition`'s answer (§5.5).
type ReviewResult struct{ Revision uint64; Stage string }
type ReviewAction string  // APPROVE · REJECT · REVOKE
type ReviewState string   // PENDING · APPROVED · REJECTED, the unset answer

// The read a client renders from: one snapshot, the selections named, the
// mask of the actor asking (§5.7).
func (c Case) View(ctx, paths ...string) (ViewResult, error)
type ViewResult struct {
    Revision uint64; Stage string; Working uint64; Roots []*ViewNode
}
type ViewNode struct {
    Path, Kind string              // address, declaration's word
    Multiple   bool                // a collection: children are rows
    Value      Value               // KindNone when the cell is unset
    Required, CanEdit, CanSubmit bool
    Reviewable bool                // a decision may be addressed *here* (§5.9)
    Review     ReviewInfo          // the unit's decision, PENDING if none
    Signable   bool                // an attestation may be made *here* (§5.10)
    Signature  SignatureInfo       // whose hand stands behind it, if any
    Options    []string            // fetched once per binding per call
    Problems   []FieldProblem      // reports about this node's own value
    Children   []*ViewNode
}

// The trail a case left, read forward from the beginning (§5.11). `cursor` 0
// is the first commit, because creating a case writes none; `limit` 0 is
// everything remaining, and a negative one is refused. An entry the actor's
// mask does not reach is off the page and in `Withheld`; the commit it
// belonged to stays, because a stage move has no entries to show and an audit
// is made of the acts. The read refuses a definition whose slot paths moved,
// as `View` and `Rebase` do — an entry keys a slot ordinal, so a relabel would
// otherwise read the whole trail through the wrong field.
func (c Case) History(ctx, cursor uint64, limit int) (HistoryResult, error)
type HistoryResult struct {
    Commits  []Commit // oldest first, the order a replay consumes
    Next     uint64   // the cursor to ask again with, 0 when the page ended
    Revision uint64   // the head, as this call read it
    Withheld int      // entries this actor may not see
}
type Commit struct {
    Revision uint64
    At       time.Time
    By       UserID
    Op       CommitOp  // SUBMIT · TRANSITION · REVIEW · SIGN
    Stage    string    // where the case stood after it
    Changes  []CommitChange
}
// Exactly one payload, the one `Kind` names. `Path` is the address `Update`
// takes back and may name a row, or even a unit, that no longer exists: an
// address that stopped resolving is the truth about a past commit (§5.1). A
// verdict's `Review` and `Signature` are the view's own shapes, and their
// author and revision are the record's rather than a payload's, which is the
// half of §5.9 and §5.10 that only a trail can say.
type CommitChange struct {
    Kind      ChangeKind // VALUE · ROWS · REVIEW · SIGN
    Path      string
    Value     Value
    Rows      []string
    Review    ReviewInfo
    Signature SignatureInfo
}

// Bringing a draft forward to the commits it has not seen, and giving it up
// instead (§5.8, DESIGN §6.5). Neither commits: the case's revision is not
// what these move.
func (c Case) Rebase(ctx) (RebaseResult, error)
func (c Case) Discard(ctx, expected uint64) error
type RebaseResult struct {
    Working, Base uint64   // the copy's number, and the revision behind it
    Conflicts     []string // on a `CONFLICT` refusal, never on a success
}

// What a stored value is, and what it holds. The getters answer a value of
// the wrong class with its zero rather than a panic or an error: a client
// rendering a form asks the declared shape first, and a field whose type a
// definition changed mid-request is a worse bug than an empty cell.
type ValueKind string // KindNone · KindText · KindInteger · KindDecimal
                      // · KindBoolean · KindDate · KindDatetime · KindBinary
                      // · KindTag · KindUser
func (v Value) Kind() ValueKind
func (v Value) Text() string
func (v Value) Int() int64
func (v Value) Decimal() decimal.Decimal
func (v Value) Bool() bool
func (v Value) Date() time.Time
func (v Value) DateTime() time.Time
func (v Value) Binary() BinaryRef
func (v Value) Tag() string
func (v Value) User() UserID

// Refusals, by category.
type Category string  // NOT_FOUND, ACCESS_DENIED, INVALID_OPERATION, …
type CaseError struct {
    Op string; Reason Category; Case CaseID; Detail string; Cause error
}
// The decision a node reports: whose judgement stands, when, and what they
// said. `State` is PENDING and the other three are zero when nothing stands
// (§5.9); `Unit` is the same on every node in the subtree, and is empty for
// content no `verifiable` declaration governs.
type ReviewInfo struct {
    Unit    string       // the governing declaration's path, never a row
    State   ReviewState
    By      UserID
    At      uint64       // the revision the decision's own commit produced
    Comment string
}

// The seam a host wires (§6). The keys are the definition's, and a
// registration replaces what was bound at that key rather than refusing.
// `path` is the §5.1 address `Update` takes back, so what a rule was told
// and what the engine later reports about it are one string.
func (r *Runtime) RegisterValidate(typeName string, fn ValidateFunc)
func (r *Runtime) RegisterOptions(fieldPath string, fn OptionsFunc)
type ValidateFunc func(ctx context.Context, at CaseContext, path string,
    v Value) (Value, error)
type OptionsFunc func(ctx context.Context, at CaseContext,
    path string) ([]string, error)

// What a hook is told about the case it was called for, and nothing more:
// no writes, no storage, no compiled definition — `Definition` is a name,
// because handing out the image would hand out a way to walk it unmasked.
// A read is answered at the *caller's* permission (§7's property 7), so a
// path the caller may not render answers the way an absent one does.
// `Revision` is the committed head; `Working` is the copy's number as the
// operation found it, which during a batch is the number the batch started
// at. The snapshot is pointers into state the store already cloned: kept
// past its call it reads the state its own batch left and never a later
// one's, which is what §6 means by a snapshot rather than a lease.
type CaseContext struct{ /* unexported */ }
func (at CaseContext) Case() CaseID
func (at CaseContext) Definition() string
func (at CaseContext) Actor() Actor
func (at CaseContext) Stage() string
func (at CaseContext) Revision() uint64
func (at CaseContext) Working() uint64
func (at CaseContext) Roles() []string
func (at CaseContext) Value(path string) (Value, bool)
func (at CaseContext) Review(path string) (ReviewInfo, bool)
func (at CaseContext) Signature(path string) (SignatureInfo, bool)

func Failed(err error, want Category) bool
```

`Definition`, `Case`, `Edit` and `Value` have no exported fields, which
is what lets §5.1's class tagging and §6's hook contract stay decisions
of this package rather than a struct literal a host assembles by hand.
`CaseContext` has none either, and for the same reason twice over: a host
that could assemble one could choose the actor and the mask it is read
at, and the type is handed to a hook by the operation that already decided
both.
The results — `ChangeResult` and the five that read like it — have the
opposite shape, and `ViewNode` follows them for the same reason §5.1
gives: a *write* a caller assembles is a value to be validated, so it
stays a constructor, while a *read* the engine assembles is data to be
rendered, so it is a struct a host may walk, marshal and diff by `Path`.
`Value` inside it keeps the rule intact: the node carries the cell, and
only this package knows what the cell's tag byte means.
`Missing` and `Invalid` are the two lists DESIGN §9 keeps out of the error
categories, and they arrive on a refusal as well as on a success: a host
renders a form's gaps without parsing a message, and reads the revision it
must reload at out of the same value.

`Review` is the third commit and the first whose commit says nothing about
data (§5.9), which is why its method takes no working copy and its result
no `Affected` set. `Sign` is the fourth and the second of both kinds (§5.10),
and it takes no working copy for the same reason and a `Missing` list for a
different one: a review refuses a caller who was not asked to decide, while
a signature refuses a form that is not ready, and readiness is a list of
paths. `History` reads the trail those four wrote (§5.11), which is the last
of §6's case-scoped reads this list was missing; what remains named there and
not here is `OnCommit`, §6.5's way of telling a user the case moved, which
waits with milestone 6's observers.

`Submit` commits content, `Transition` commits a stage, `Review` commits a
verdict about content, `Sign` commits a verdict about a *revision*, `View`
reads all four beside an actor's own draft, and §5.8's two operations settle
a draft against what the case committed after it was made.

`BinaryRef` is exported as a `[]byte` with no behavior. DESIGN §5.2 puts
content handles in case *data*, and §5.1's value row reserves the field
now so a row never changes shape when the binary store arrives; nothing
in this package reads or resolves one.

- `schema` remains the only consumer of source text and exports nothing
  beyond its diagnostics and resolved model (SYNTAX §5.8). `Load` calls it
  once and never re-runs it, whether or not lowering succeeds.
- A host never imports `schema` to read a refusal. `Problem` mirrors the
  checker's diagnostic — position, code, the name it singled out — and
  carries the front end's own sentence in `Msg`, so codes are branched on
  as the document spells them (`"UNKNOWN_TYPE"`) and text is displayed
  without a second table. The conversion is field-by-field, which makes it
  total: a code added to the front end cannot be forgotten at the seam.
- Lowering reads only exported `Schema` fields and treats the §4
  invariants as assumptions. A violated assumption is an internal
  defect: reported through the `SCHEMA_MISMATCH` category with the
  offending slot (DESIGN §9), never laundered into data corruption.
- At the definition seam one error type carries two causes, and the cause
  is not something a host has to parse out of a message. `Problems` is
  what the author must fix, with the codes and positions the front end
  chose; `Defects` is what the runtime must fix, and `Mismatch` says
  which. Either way `Load` returns
  no definition: a refused definition has no image at all (§7.5).
- `Definition` is immutable and safe for concurrent use, because nothing
  in it is written after `Load` returns (§7.4). Warnings are reported and
  kept (§4): they never invalidate a definition, and they are the only
  thing about a loaded definition an author would still want to read.
- Duplicate definition IDs (DESIGN §3) are a registry rule, and
  `Runtime.Register` is where it lives: two `Load`s of one source yield
  two equivalent, independent definitions, and registering the second
  name under the same ID is the host's mistake to hear about. `Replace`
  is the separate act that says so — re-registering a definition under a
  live ID reinterprets every case that names it, which is why `Create`
  and `Open` read through the registry rather than holding a definition
  pointer of their own.
- The ceilings are checked where the ordinal or mask they protect is
  assigned — 64 roles, 64 top-level forms, 255 stages, 65535 types and
  tags, four levels of `multiple` nesting (§3.5, §3.9) — and each is
  reported as an internal defect, never as a widened word or a wrapped
  index. A build that has already refused something skips §4's drift
  checks: its arrays are short by design, and walking them would report
  a second defect about the wrong node.
- Conformance gate: the SYNTAX §7 example must (a) compile with zero
  diagnostics, (b) lower to the shape traced in Appendix A, and
  (c) execute Appendix A's runtime steps as a test once milestone 2
  lands. (a) and (b) are `TestSpecExample*` in `lower_test.go`, with the
  source read out of SYNTAX.md so the fixture cannot drift from the
  document it is traced against; (a) is also checked through `Load`, so
  the seam is not merely documentation. (c) is arriving with the
  operations it traces rather than as one final test: §5.2's presence and
  requiredness is `TestAppendixARequiredness`, and the trace's
  `Transition` steps 1 through 6 — role set from a committed assignment,
  edge, mask, guard, the destination's `require`, and the commit that
  moves only the stage — are `TestAppendixATransitionTrace`, which also
  checks the two `permTables` answers the trace ends on. Step 5's approval
  conjunct is part of that trace, which is why the test performs a
  `Review` of the required form before it asks for the move: SYNTAX §7's
  walkthrough says the case goes to `company_review` only after the
  `Registrar` or `Manager` approves, so the gate is checked where the spec
  states it rather than in a fixture of its own. Step 5's signature
  conjunct is checked the same way, in `TestAppendixARequiredSignatureTrace`
  beside it: the shipped §7 example marks no signature `required`, so the
  trace above asserts that the third scan wants nothing, and the second
  runs the same corpus with one word added to make the attestation
  something the destination asks for. Both shapes are in the gate because
  both are in the language, and a single trace could only have shown one.
  The trace's step 8 — the trail the second script left, read through the
  exported seam — is `TestAppendixAHistoryTrace`, and it belongs in the gate
  for the reason the two conjuncts do: §5.11's read is where the entries the
  other steps wrote turn out to have been worth writing, so the corpus is
  asked what it *recorded* as well as what it decided. Step 7's `OnCommit` is
  what still waits for milestone 6.
  §6.5's synchronization has no step in this trace, because the trace is one
  actor moving one case forward: it is tested as the two actors §6.5
  describes, against the exported surface, in `rebase_test.go` (§5.8).

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
   `application` is verifiable as a whole, so the unit is the form and
   `verif[holderName] = verif[shareholders]` from the lowering block above
   is what makes its nested fields answer through it — `submitted`
   standing against the content it bound, and `approved_by` standing iff
   the committed `reviewer` user signed that content. All three halves run
   today. The shipped §7 marks neither signature `required`, so the third
   scan finds two slots and asks neither of them, which
   `TestAppendixATransitionTrace` asserts as an empty list; the conjunct's
   live shape is `TestAppendixARequiredSignatureTrace` beside it (§5.5).

   The approval half is why this trace has a step the spec's listing does
   not: `Review(holderOfManager, "application", APPROVE, observed, "")`
   stands before the move. The trace's actor is the committed
   `application.reviewer`, so `draft`'s `accept, reject` grant and
   `Registrar`'s and `Manager`'s `review application` grant are the same
   person's, which is exactly the definition SYNTAX §7 writes and the
   reason one fixture serves both steps. A `Transition` attempted at the
   same revision with no decision committed is refused and names
   `application` on its `Missing` list — SYNTAX §7's walkthrough says the
   case reaches `company_review` *after* the approval, and step 5's
   conjunct is what makes the runtime agree.
6. Revision check against the global committed revision (DESIGN §6.4),
   then one atomic forward commit: the stage change, plus whatever §5.6's
   two effects map to. Here that is nothing — the move writes one entry
   about the stage, and the driver asks an effect about content only. The
   `Review`'s decision two revisions back is not a content entry either,
   so the decision that satisfied step 5 survives the move that consumes
   it.
   The shipped call asks the number *before* steps 4 and 5, because both
   read content and content the caller has not seen is content it cannot
   be told to fix (§5.5's order); the trace keeps the spec's listing.
7. `OnCommit` fires after the commit is durable with the actual
   `Commit` and post-commit frame (§6), never inside the transaction.
   Milestone 6's, like the same step after a submit.
8. `History(cursor 0, limit 0)` reads the trail those steps left, and it is the
   step that turns the other seven into claims: the entries §5.6 says a commit
   records are rendered for a caller who was not the actor that made the call.
   The second trace's script, with one commit added — the archive the new stage
   made the `Registrar` editable — answers with six records in the order the
   calls happened, each carrying its own hand, its operation word and the stage
   the case stood at after it: content, a decision, an attestation, an edge,
   content again, and content in a form the Applicant may not see. The
   transition record holds no entries at all, because what a stage move's diff
   is is the stage, and the trail is its only record. The decision's entry is
   this section's own `ReviewInfo` at the unit, comment and all, and the
   attestation's is `SignatureInfo` with `reviewer-note`; both take author and
   revision from the record's envelope, which is what lets the Applicant read a
   decision she was never asked to make.

   Two halves of §5.11 are checkable only here. The attestation step 5's third
   conjunct demanded, which a later content commit then erased from the state,
   survives in the trail: the state answers with nothing and the page answers
   with the hand, the revision and the reference, which is the sentence §5.10
   could write only as a promise until a reader existed. And the Applicant's
   page holds all six records with two entries off it and `Withheld` at two,
   because §5.7's mask reaches `application` and not `archive` — the record of
   an act outlives its content, and the count says this page is not the whole
   story without naming the declaration it does not name. The addresses the
   page printed are the ones `Update` takes back, so the step writes one
   recorded field and one recorded instance into a new batch.

The trace touches: three integer sets (role set, accept mask, requires
bitset), one `condFn`, one decision-table read and the subtree walks
bounded by definition size. Step 8 adds one mask fold, plus index arithmetic
on the trail and a render per entry the page answers with. That decision-table
read is step 5's approval conjunct, and it is a slot-field lookup plus an
array index — the reason §5.5 can say the conjunct arrives "as further gaps on
the same list rather than as new shapes" and mean it in the machine as well as
in the document. The signature conjunct beside it is the same shape one field
over: two reads of `slots[n].sig` for the form's span and one index into a
table that this case never fills, since §7 marks neither field `required`
(step 5).
§3.9 turned the caller's `"company_review"` into a `stageID` before any
of it ran, so the interpretation itself does no name comparison and no
type dispatch, and probes no map — a case's only data-keyed lookup is a
collection's instance index (§5.1), and a transition that names no
instance never reaches it.
