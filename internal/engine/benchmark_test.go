package engine

// RUNTIME §8's harness. What is measured here is what is shipped:
// lowering, the two hot reads the image exists to make cheap — one
// condition closure, one permission resolve — path resolution, the
// fanout on its own, one accepted edit batch, the two operations that
// commit, and the read that renders them. Nothing is faked: a benchmark
// that hand-builds a state and calls one internal function measures the
// fixture rather than a request, which is the one claim §8's rows have
// to be able to survive.
//
// A row is measured against something. Where the pair is not obvious from
// the name, the benchmark's own comment says what its sub-benchmarks are
// the two ends of, because a number with no comparison is folklore with a
// timestamp on it.
//
// These are not pass/fail tests. They exist so the cost claims in §2.2
// and §8 stay assertions someone re-runs rather than folklore.

import (
	"strconv"
	"testing"

	"github.com/monshihq/monshi/internal/program"
	"github.com/monshihq/monshi/internal/testfixture"
	"github.com/shopspring/decimal"
)

// mustLower is compile() without a *testing.T to insist on, and a TB so a
// test can build the same fixture a benchmark times.
func mustLower(tb testing.TB, id, src string) *program.Program {
	tb.Helper()
	img, err := program.Compile(id, testfixture.Parse(tb, src))
	if err != nil {
		tb.Fatalf("lower: %v", err)
	}
	return img
}

// BenchmarkCondEval runs the §7 example's own guard: internal
// present[.kind == COMPANY], which lowers to one tagCmp closure (§3.4).
// The state view is built once, so what is timed is one presence test and
// one ordinal compare through a closure — the whole cost of a condition
// at request time.
func BenchmarkCondEval(b *testing.B) {
	img := mustLower(b, "bench", specExample(b))
	kind := findSlot(img, "application.kind")
	internal := findSlot(img, "application.internal")
	c := img.Cond(img.Slot(internal).CondPres)
	if c == nil {
		b.Fatal("application.internal lost its presence guard")
	}
	view := make(program.Row, img.SlotCount())
	view[kind] = program.Cell{Set: true,
		Int: int64(img.Type(img.Slot(kind).Type).Tags[1])} // COMPANY
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !c(view) {
			b.Fatal("COMPANY must satisfy the guard")
		}
	}
}

// BenchmarkPermResolve asks the §3.5 question for every slot at once:
// bits for one role, in one stage, with the overlay probed. The table
// reports ns per slot, because "2 bitmap ops per node" is the claim
// being measured, not the loop around it.
func BenchmarkPermResolve(b *testing.B) {
	img := mustLower(b, "bench", testfixture.Wide(b))
	role := findRole(img, "R1")
	stage := findStage(img, "s1")
	roles := []program.RoleID{role} // the actor's set is a request fact, not a loop body
	n := img.SlotCount()
	var held uint8
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		held = 0
		for s := range img.SlotCount() {
			held |= img.Effective(roles, stage, program.NodeID(s))
		}
	}
	if held&program.PermView == 0 {
		b.Fatal("R1 holds no view bits")
	}
	// One op is the whole walk, so the per-slot figure is this loop's
	// cost divided by the slots it touched.
	b.StopTimer()
	b.ReportMetric(float64(n), "slots/op")
}

func findSlot(img *program.Program, path string) program.NodeID {
	for i := range img.SlotCount() {
		if img.Path(program.NodeID(i)) == path {
			return program.NodeID(i)
		}
	}
	panic("no slot at " + path)
}

func findRole(img *program.Program, name string) program.RoleID {
	id, ok := img.RoleByName(name)
	if !ok {
		panic("no role " + name)
	}
	return id
}

func findStage(img *program.Program, name string) program.StageID {
	id, ok := img.StageByName(name)
	if !ok {
		panic("no stage " + name)
	}
	return id
}

// BenchmarkUpdate is §8's request-level row: one accepted batch of three
// edits through the exported operation, on the SYNTAX §7 definition and a
// case create made. What it measures is what a host pays — resolve, the
// permission fold, three settles, and the store's clone-and-guard — rather
// than one internal function reached by a fixture.
//
// The three paths are one per payload shape that costs something to check: a
// plain string, an enum tag resolved against its owning type, and a decimal
// against its declared bounds. `Applicant` is the actor the §7 roles block
// grants the form to, so the batch is accepted and the guard is the store's
// own revision, threaded forward as a host would.
func BenchmarkUpdate(b *testing.B) {
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "bench",
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}

	base, fee := uint64(0), decimal.NewFromInt(1500)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := c.Update(ctx(), base,
			Set("application.full_name", Text("Monshi Ltd")),
			Set("application.kind", Tag("COMPANY")),
			Set("application.fee", Decimal(fee)))
		if err != nil {
			b.Fatalf("Update: %v", err)
		}
		base = res.Working
	}
}

// BenchmarkUpdateFanout measures §5.3's walk on its own, in four shapes, so the
// two claims the section makes are two comparisons rather than one number.
//
// Sized by the definition, not by the batch: `spec/floor` writes `full_name`, a
// field no guard in the §7 example reads, and `wide/floor` writes the same
// nothing against the 51-slot corpus. Same work, 20 slots versus 51, and the
// difference is the visited array and queue that `depBound` fixed at lowering —
// which is also why the *edge* count barely appears: `spec/guard` crosses both
// edges the small definition has, from `kind` to the group and the collection
// its tag decides the presence of, expands each to its subtree, and collapses
// them back on the way out. `spec/all` is the ceiling, and a ceiling on the same
// image as the floor is what makes the pair readable: a closure from any subset
// visits a subset of these edges and enqueues a subset of these slots.
//
// The allocations in every row are those two arrays plus the rendered paths: the
// walk allocates nothing per edge, and no path string is built for a slot the
// collapse drops.
func BenchmarkUpdateFanout(b *testing.B) {
	spec := mustLower(b, "spec", specExample(b))
	wide := mustLower(b, "wide", testfixture.Wide(b))
	floor := findSlot(spec, "application.full_name")
	guarded := findSlot(spec, "application.kind")
	unread := findSlot(wide, "f.s1")

	b.Run("spec", func(b *testing.B) {
		b.Run("floor", func(b *testing.B) { walkOne(b, spec, floor) })
		b.Run("guard", func(b *testing.B) { walkOne(b, spec, guarded) })
		b.Run("all", func(b *testing.B) { walkAll(b, spec) })
	})
	b.Run("wide", func(b *testing.B) {
		b.Run("floor", func(b *testing.B) { walkOne(b, wide, unread) })
	})
}

// walkOne runs the walk for a single-field write set. These are functions rather
// than three loops so the reporting and each sub-benchmark's scope stay in one
// place and the cases differ only in what they mark.
func walkOne(b *testing.B, img *program.Program, wrote program.NodeID) {
	b.Helper()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f := newFanout(img)
		f.wroteField(wrote)
		if len(f.affected()) == 0 {
			b.Fatal("the walk reported nothing for a slot it was handed")
		}
	}
}

// walkAll is the upper-bound case: every declaration marked, as a batch that
// rewrote a form field by field would. A collection is marked as a row arriving,
// which is the wider of its two verbs, and the slot count comes from the image
// rather than from the corpus's description — the point of the row is the bound.
func walkAll(b *testing.B, img *program.Program) {
	b.Helper()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f := newFanout(img)
		for n := range img.SlotCount() {
			if img.Slot(program.NodeID(n)).Coll != 0 {
				f.wroteRows(program.NodeID(n))
			} else {
				f.wroteField(program.NodeID(n))
			}
		}
		if len(f.affected()) == 0 {
			b.Fatal("marking every declaration reported nothing")
		}
	}
}

// benchSpecCase is a runtime and one Applicant's handle on a case of the §7
// corpus, which is what both commit benchmarks below drive. Built the way a
// host builds it — `Create`, then operations on the handle — so the numbers
// are a request's and not a fixture's.
func benchSpecCase(b *testing.B) Case {
	b.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "bench",
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	return c
}

// A complete `application` in three writes: the two required fields, and a
// fee that differs every call so the diff has something to say. `PERSON`
// keeps the conditional collection and the gated subtree out of the walk,
// which is the cheap case — the completeness pass asks the §7 example's whole
// preorder range either way, and pruning is what it declines to answer.
func completeApplication(i int) []Edit {
	return []Edit{
		Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Monshi Ltd")),
		Set("application.fee",
			Decimal(decimal.NewFromInt(1500+int64(i)%500))),
	}
}

// BenchmarkSubmit measures the commit path (RUNTIME §5.4, DESIGN §6.3) as the
// request a host actually makes: a batch that changes one form, then the
// submit of that form. What it charges, in order, is the copy's read, the
// completeness walk, `Validate` re-run over the subtree, the forward diff, and
// then the store's three consistent writes — of which one is a clone of the
// committed snapshot, because §5.1's state is materialized per case rather
// than per revision and a commit has to leave the old one intact for whoever
// is reading it.
//
// That clone is why the pair is not "an update plus a flag", and milestone 4
// will not make it cheaper: the transaction changes how the three writes are
// made durable, not how many bytes the new snapshot holds.
//
// The tail this row now also carries is §5.3's walk over the entries the
// commit recorded, which is what `CommitResult.Affected` is made of (§9):
// four objects on a submitting corpus, and the row in §8 says which four.
func BenchmarkSubmit(b *testing.B) {
	c := benchSpecCase(b)
	base := uint64(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := c.Update(ctx(), base, completeApplication(i)...)
		if err != nil {
			b.Fatalf("Update: %v", err)
		}
		base = res.Working
		if _, err := c.Submit(ctx(), "application", base); err != nil {
			b.Fatalf("Submit: %v", err)
		}
	}
}

// BenchmarkSubmitNoOp measures the half of §5.4 that runs when the answer is
// "nothing to write": every check passes, the diff is empty, and the case's
// revision and history stay where they were. The gap between this and
// `BenchmarkSubmit` minus an update is what a commit costs on top of deciding
// that one is needed — the clone, the entry and the rebase.
func BenchmarkSubmitNoOp(b *testing.B) {
	c := benchSpecCase(b)
	res, err := c.Update(ctx(), 0, completeApplication(0)...)
	if err != nil {
		b.Fatalf("Update: %v", err)
	}
	base := res.Working
	// One accepted commit first, so every timed call is the one with nothing
	// left to say.
	if _, err := c.Submit(ctx(), "application", base); err != nil {
		b.Fatalf("Submit: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := c.Submit(ctx(), "application", base); err != nil {
			b.Fatalf("Submit: %v", err)
		}
	}
}

// benchSpecReview is the §7 corpus's two-edge loop out of and back into
// `draft`: a Manager rejects to `correction`, and `correction` accepts back to
// `draft`. Both edges are unconditional and neither destination names a
// `require`, so one call is one commit with nothing else on its tab — no
// completeness walk, no diff, no grafted content. That is the pair the two
// benchmarks below split apart: the same decision path, once allowed and once
// stopped by the edge's own condition.
//
// The actor is a Manager by primary role rather than the committed reviewer,
// because conferring the role through `application.reviewer` would mean
// committing content inside the timed loop.
func benchSpecReview(b *testing.B) Case {
	b.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "bench",
		Actor{User: "elliot", PrimaryRole: "Manager"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	return c
}

// BenchmarkTransition measures a stage move end to end (RUNTIME §5.5, DESIGN
// §6.4): the role fold, the edge lookup, one mask test, the revision compare,
// the destination's `require` — nothing, for these two stages — the view walk,
// and then the store's three consistent writes. It is the commit path minus
// §5.4's two walks and minus §5.6's diff, which is the honest way to say what
// a transition costs: it pays for a commit and for neither of the passes that
// decide whether a commit is warranted.
func BenchmarkTransition(b *testing.B) {
	c := benchSpecReview(b)
	dest := [2]string{"correction", "draft"}
	rev := uint64(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := c.Transition(ctx(), dest[i%2], rev)
		if err != nil {
			b.Fatalf("Transition to %q: %v", dest[i%2], err)
		}
		rev = res.Revision
	}
}

// BenchmarkTransitionGuard measures the same call stopped at its content check:
// nothing is committed, so `application.kind` is unset and the PERSON edge's
// guard answers false. The path is therefore the fold, the lookup, the mask,
// the compare and one `condFn` over one row — and no require walk, no view walk
// and no write. The refusal is returned as a `*CaseError`, so this row includes
// the error's own allocation; what it isolates is that the checks before a
// commit are a fraction of what the commit is.
func BenchmarkTransitionGuard(b *testing.B) {
	c := benchSpecReview(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, err := c.Transition(ctx(), "person_review", 0)
		if !Failed(err, InvalidOp) {
			b.Fatalf("the unmet guard answered %v, want INVALID_OPERATION",
				err)
		}
	}
}

// benchWideCase is §8's corpus as a live case: a role that sees the whole
// definition, a draft with content in it, and rows in the collection, so the
// walk below is asked about data and not about an empty frame. Everything is
// prepared outside the timed loop — the batch, the rows and the values are the
// fixture, the way a host's own state would be.
func benchWideCase(tb testing.TB, rows int) Case {
	tb.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(tb, "wide", testfixture.Wide(tb))}
	if err := rt.Register(d); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "wide",
		Actor{User: "dana", PrimaryRole: "R1"})
	if err != nil {
		tb.Fatalf("Create: %v", err)
	}
	edits := []Edit{
		Set("f.i0", Int(1)),
		Set("f.s1", Text("Monshi Ltd")),
		Set("f.d2", Decimal(decimal.NewFromInt(1500))),
		Set("f.b3", Bool(true)),
	}
	for i := 0; i < rows; i++ {
		edits = append(edits, Add("f.rows"))
	}
	if _, err := c.Update(ctx(), 0, edits...); err != nil {
		tb.Fatalf("Update: %v", err)
	}
	// The warm-up read is not decoration: it is the one proof that the
	// fixture is a case the operation can answer at all, which a timed loop
	// would otherwise learn by failing in a way that looks like a number.
	if _, err := c.View(ctx()); err != nil {
		tb.Fatalf("View: %v", err)
	}
	return c
}

// BenchmarkViewWide is §8's last request-level row: the whole visible case read
// at once, on the corpus §8 calls the worst locality profile a traversal can
// have. Every one of the 51 slots is visible to this actor, so `visible()` pays
// its full scan; four fields hold values and the collection holds rows, so the
// walk reads the draft at instance addresses rather than answering from
// declaration cells alone.
//
// The second sub-case is the comparison that makes the number mean something:
// one field asked for by path, which is the same operation with a selection of
// one and a subtree walk of one. The gap is what the *walk* costs — the
// visibility pass, the permission fold and the snapshot clone are per call and
// not per node, so they appear in both rows and the difference is the nodes.
func BenchmarkViewWide(b *testing.B) {
	b.Run("all", func(b *testing.B) {
		slots := mustLower(b, "wide", testfixture.Wide(b)).SlotCount()
		c := benchWideCase(b, 4)
		pre, err := c.View(ctx())
		if err != nil {
			b.Fatalf("View: %v", err)
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.View(ctx())
			if err != nil {
				b.Fatalf("View: %v", err)
			}
			if len(res.Roots) != 1 {
				b.Fatalf("the wide corpus has %d roots", len(res.Roots))
			}
		}
		// Nodes and slots are both reported because §8's per-node figures
		// divide by the former and the corpus is described in the latter:
		// instances multiply a group's members into more nodes than the
		// definition has slots, and a row that reads well against the wrong
		// denominator is how a cost claim becomes folklore.
		b.StopTimer()
		b.ReportMetric(float64(slots), "slots/op")
		b.ReportMetric(float64(countNodes(pre.Roots)), "nodes/op")
	})
	b.Run("one", func(b *testing.B) {
		c := benchWideCase(b, 4)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.View(ctx(), "f.i0")
			if err != nil {
				b.Fatalf("View: %v", err)
			}
			if len(res.Roots) != 1 {
				b.Fatalf("one path returned %d roots", len(res.Roots))
			}
		}
	})
}

// countNodes counts the nodes a view rendered, which is the denominator §8's
// per-node cost is stated against. It is a helper rather than an assertion:
// the number changes with the corpus's row count, and a benchmark that failed
// on it would be a benchmark that cannot be re-run after a fixture edit.
func countNodes(nodes []*ViewNode) int {
	n := 0
	for _, x := range nodes {
		n++
		n += countNodes(x.Children)
	}
	return n
}

// benchSyncPair is one §7 case seen by two Applicant sessions of two
// different users: the shape DESIGN §6.5 exists for, where one holds a draft
// and the other has just committed past it. The role is Applicant because
// §7 grants it `view, edit, submit application` at `draft` and `correction`,
// and the loop below submits rather than transitions.
func benchSyncPair(tb testing.TB) (dana, elliot Case) {
	tb.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(tb, "bench", specExample(tb))}
	if err := rt.Register(d); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	dana, err := rt.Create(ctx(), "bench",
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		tb.Fatalf("Create: %v", err)
	}
	elliot, err = rt.Open(ctx(), dana.ID(),
		Actor{User: "elliot", PrimaryRole: "Applicant"})
	if err != nil {
		tb.Fatalf("Open: %v", err)
	}
	return dana, elliot
}

// BenchmarkRebaseClean measures §5.8's merge when the answer is silence: a
// draft holding an edit in a field the commits never touch, against a case
// that has moved by exactly one value commit. Every cell in the window is
// taken and nothing is asked of the user, which is the shape §6.5 promises
// will not need a human. The comparison is `BenchmarkRebaseSynced` below, and
// the honest further subtraction is `BenchmarkSubmit`: this row is that plus
// the walk over the unseen record, the clone the merge runs on, the cell
// comparisons, and the guarded save that moves the lineage.
//
// The per-iteration commit is not padding. A rebase with no window is the
// synchronized row, and a rebase measured against a history that never moves
// would be measuring a cache hit.
func BenchmarkRebaseClean(b *testing.B) {
	dana, elliot := benchSyncPair(b)
	// One complete application, committed before the draft exists, so the
	// first timed window is a one-field change on top of a real case.
	res, err := elliot.Update(ctx(), 0, completeApplication(3)...)
	if err != nil {
		b.Fatalf("the committed batch: %v", err)
	}
	if _, err := elliot.Submit(ctx(), "application", res.Working); err != nil {
		b.Fatalf("the first commit: %v", err)
	}
	eW := res.Working
	if _, err := dana.Update(ctx(), 0,
		Set("application.note", Text("half-finished"))); err != nil {
		b.Fatalf("the draft's batch: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := elliot.Update(ctx(), eW, Set("application.fee",
			Decimal(decimal.NewFromInt(1000+int64(i)%500))))
		if err != nil {
			b.Fatalf("Update: %v", err)
		}
		eW = res.Working
		if _, err := elliot.Submit(ctx(), "application", eW); err != nil {
			b.Fatalf("Submit: %v", err)
		}
		if _, err := dana.Rebase(ctx()); err != nil {
			b.Fatalf("Rebase: %v", err)
		}
	}
}

// BenchmarkRebaseSynced measures the same call when the copy already descends
// from the case's newest revision: no merge, no save, and §5.8's claim that a
// redundant rebase writes nothing. What remains is the read of the draft —
// the look-up, the fold, and the snapshot clone — which is why this is the
// floor the rows above and below it are stated against.
func BenchmarkRebaseSynced(b *testing.B) {
	dana, _ := benchSyncPair(b)
	if _, err := dana.Update(ctx(), 0,
		Set("application.note", Text("half-finished"))); err != nil {
		b.Fatalf("the draft's batch: %v", err)
	}
	if _, err := dana.Rebase(ctx()); err != nil {
		b.Fatalf("the rebase that makes the copy synchronized: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := dana.Rebase(ctx()); err != nil {
			b.Fatalf("Rebase: %v", err)
		}
	}
}

// BenchmarkRebaseRows is the same merge with the window's commit moving a row
// list: one shareholder in and one out every iteration, so the case's work
// churns while its count — and the clone the merge pays for — stays the same
// size. The draft has not touched the rows, which puts every call on the
// branch that adopts the committed list, the only row policy in §5.8 that
// copies content rather than comparing it. The comparison is
// `BenchmarkRebaseClean`, whose window holds one value and no rows: the gap
// is the set entry's base resolution, the list comparison, and the row copies
// that `apply` makes behind it.
func BenchmarkRebaseRows(b *testing.B) {
	dana, elliot := benchSyncPair(b)
	res, err := elliot.Update(ctx(), 0,
		Set("application.kind", Tag("COMPANY")),
		Set("application.full_name", Text("Monshi Ltd")),
		Set("application.fee", Decimal(decimal.NewFromInt(1500))))
	if err != nil {
		b.Fatalf("the COMPANY batch: %v", err)
	}
	eW := res.Working
	first := ""
	for r := 0; r < 2; r++ {
		res, err = elliot.Update(ctx(), eW, Add("application.shareholders"))
		if err != nil {
			b.Fatalf("the committed row: %v", err)
		}
		eW = res.Working
		row := res.Created[0]
		if r == 0 {
			first = row
		}
		res, err = elliot.Update(ctx(), eW,
			Set(row+".holder_name", Text("Holder")),
			Set(row+".shares", Decimal(decimal.NewFromInt(1))))
		if err != nil {
			b.Fatalf("the committed row's fields: %v", err)
		}
		eW = res.Working
	}
	if _, err := elliot.Submit(ctx(), "application", eW); err != nil {
		b.Fatalf("the first commit: %v", err)
	}
	if _, err := dana.Update(ctx(), 0,
		Set("application.note", Text("half-finished"))); err != nil {
		b.Fatalf("the draft's batch: %v", err)
	}
	drop := first
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		// One row in and one out: the list changes by membership, and the
		// draft's own claim on it stays exactly as thin as it started.
		res, err := elliot.Update(ctx(), eW,
			Add("application.shareholders"), Remove(drop))
		if err != nil {
			b.Fatalf("the row batch: %v", err)
		}
		eW = res.Working
		drop = res.Created[0]
		res, err = elliot.Update(ctx(), eW,
			Set(drop+".holder_name", Text("Holder")),
			Set(drop+".shares", Decimal(decimal.NewFromInt(1))))
		if err != nil {
			b.Fatalf("the new row's fields: %v", err)
		}
		eW = res.Working
		if _, err := elliot.Submit(ctx(), "application", eW); err != nil {
			b.Fatalf("Submit: %v", err)
		}
		if _, err := dana.Rebase(ctx()); err != nil {
			b.Fatalf("Rebase: %v", err)
		}
	}
}

// BenchmarkDiscard is §6.5's other exit: the drop of a draft that holds work.
// The pair is the shape of the same call with nothing behind it — an actor
// with no copy, which is the one discard §5.8 says is already true — so the
// gap is the guarded delete, and what the loop's batch costs is charged to
// `BenchmarkUpdate` by comparison rather than hidden here.
func BenchmarkDiscard(b *testing.B) {
	b.Run("draft", func(b *testing.B) {
		dana, _ := benchSyncPair(b)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := dana.Update(ctx(), 0, Set("application.note",
				Text("unfinished work "+strconv.Itoa(i))))
			if err != nil {
				b.Fatalf("the batch that makes a draft: %v", err)
			}
			if err := dana.Discard(ctx(), res.Working); err != nil {
				b.Fatalf("Discard: %v", err)
			}
		}
	})
	b.Run("absent", func(b *testing.B) {
		dana, _ := benchSyncPair(b)
		if err := dana.Discard(ctx(), 0); err != nil {
			b.Fatalf("the warm-up discard: %v", err)
		}
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if err := dana.Discard(ctx(), 0); err != nil {
				b.Fatalf("Discard: %v", err)
			}
		}
	})
}

// BenchmarkReview is a decision end to end (RUNTIME §5.9, DESIGN §6.4): the
// role fold, the §3.9 resolution, the mask test, the unit test, the revision
// compare, and then a commit whose diff is one entry. The loop alternates
// approve and revoke so that every iteration commits — a repeated decision is
// refused work by §6.3's rule, so a benchmark that asked for the same approval
// twice would be measuring `BenchmarkReviewNoOp` and calling it a commit.
//
// The content stays empty on purpose: `Review` decides what the case holds and
// asks nothing about completeness, which is §5.4's question at `Submit` and
// §5.5's at a destination's `require`.
//
// The result's `Affected` seeds the walk's cheapest shape (§9): the verdict
// entry re-asked, the decided unit rendered, and whatever the same commit's
// reset moved arriving with them.
func BenchmarkReview(b *testing.B) {
	c := benchSpecReview(b)
	rev := uint64(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		act := ReviewApprove
		if i%2 == 1 {
			act = ReviewRevoke
		}
		res, err := c.Review(ctx(), "application", act, rev, "")
		if err != nil {
			b.Fatalf("Review %s: %v", act, err)
		}
		rev = res.Revision
	}
}

// BenchmarkReviewNoOp is the request that is already true: the same decision by
// the same hand with the same comment, answered with success and no commit. The
// gap to `BenchmarkReview` is what the store's three writes, the table copy and
// the reset cost — everything a decision does rather than decides.
func BenchmarkReviewNoOp(b *testing.B) {
	c := benchSpecReview(b)
	res, err := c.Review(ctx(), "application", ReviewApprove, 0, "")
	if err != nil {
		b.Fatalf("the decision every timed call repeats: %v", err)
	}
	rev := res.Revision
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		again, err := c.Review(ctx(), "application", ReviewApprove, rev, "")
		if err != nil {
			b.Fatalf("Review: %v", err)
		}
		if again.Revision != rev {
			b.Fatalf("the repeat committed at %d, want %d", again.Revision, rev)
		}
	}
}

// BenchmarkReviewRefused is the same call stopped at the door, and the row that
// says what an unauthorized decision costs a host: the fold, the resolution, one
// mask test and a refusal. It answers before the unit test and before the
// revision compare, which is §7's property 7 for this operation — so the
// message's allocations are the whole bill, and no case fact is read to build
// it. The actor holds the primary role the definition never granted.
func BenchmarkReviewRefused(b *testing.B) {
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "bench", Actor{User: "bo",
		PrimaryRole: "Auditor"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := c.Review(ctx(), "application", ReviewApprove,
			uint64(i), ""); err == nil {
			b.Fatal("the Auditor's review was granted")
		}
	}
}

// benchGateCase builds a SYNTAX §7 case whose `application` is complete and
// APPROVED, and hands back the manager who may move it. The applicant writes
// the content and the manager decides about it, because §7 grants `submit` to
// one and `review` to the other, and a fixture that faked either with a role
// the definition does not hold would be measuring a different case.
//
// The revision returned is the one the manager's own decision committed at:
// every timed call starts from it.
func benchGateCase(b *testing.B) (Case, uint64) {
	b.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "gate", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	dana, err := rt.Create(ctx(), "gate",
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	res, err := dana.Update(ctx(), 0,
		Set("application.kind", Tag("PERSON")),
		Set("application.full_name", Text("Monshi Ltd")))
	if err != nil {
		b.Fatalf("Update: %v", err)
	}
	if _, err = dana.Submit(ctx(), "application", res.Working); err != nil {
		b.Fatalf("Submit: %v", err)
	}
	mgr, err := rt.Open(ctx(), dana.ID(),
		Actor{User: "elliot", PrimaryRole: "Manager"})
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	rev, err := mgr.Revision(ctx())
	if err != nil {
		b.Fatalf("Revision: %v", err)
	}
	said, err := mgr.Review(ctx(), "application", ReviewApprove, rev, "")
	if err != nil {
		b.Fatalf("Review: %v", err)
	}
	return mgr, said.Revision
}

// BenchmarkTransitionRequires is `BenchmarkTransition`'s gated twin: the same
// three commits per iteration, one of which moves into a stage that declares
// `require application` and so runs §5.5's completeness pass and approval
// scan. The reject to `correction` and the accept back to `draft` ask nothing
// — a reject skips completeness, and neither of those stages requires a form —
// so the row is one gated move in three undated ones, and the claim made from
// it is the difference to `BenchmarkTransition` rather than an absolute.
func BenchmarkTransitionRequires(b *testing.B) {
	mgr, rev := benchGateCase(b)
	dest := []string{"person_review", "correction", "draft"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		res, err := mgr.Transition(ctx(), dest[i%3], rev)
		if err != nil {
			b.Fatalf("Transition to %q: %v", dest[i%3], err)
		}
		rev = res.Revision
	}
}

// benchSpecSign is a case ready to be signed: the content §7's required fields
// ask for, the reviewer the form names in it, and the approval the form's own
// `verifiable` declaration makes a precondition of an attestation about it. Two
// actors, because §4.2's readiness test is about content and a judgement other
// hands made, and a benchmark that skipped it would be measuring a call the
// operation cannot make.
//
// The content is complete here and empty in `BenchmarkReview` for the one
// difference between the two operations: `Review` decides what the case holds
// and asks nothing else of it, while `Sign` asks the completeness walk and the
// pending-unit scan before it asks the store for anything. The subtraction that
// reads this row against `BenchmarkSubmit` is therefore the gate's walk, the
// table copy and the entry, and not a clean commit.
func benchSpecSign(b *testing.B) (Case, uint64) {
	b.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	dana, err := rt.Create(ctx(), "bench",
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	edits := append(completeApplication(0),
		Set("application.reviewer", User("elliot")))
	up, err := dana.Update(ctx(), 0, edits...)
	if err != nil {
		b.Fatalf("Update: %v", err)
	}
	sub, err := dana.Submit(ctx(), "application", up.Working)
	if err != nil {
		b.Fatalf("Submit: %v", err)
	}
	elliot, err := rt.Open(ctx(), dana.ID(), Actor{User: "elliot",
		PrimaryRole: "Manager"})
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	rvs, err := elliot.Review(ctx(), "application", ReviewApprove,
		sub.Revision, "")
	if err != nil {
		b.Fatalf("Review: %v", err)
	}
	return elliot, rvs.Revision
}

// BenchmarkSign is an attestation end to end (RUNTIME §5.10, DESIGN §4.2): the
// fold, the resolution, the mask and the signature entry's own conjunct, the
// readiness gate's two scans, the revision compare, and then a commit whose diff
// is one entry. The loop alternates the evidence reference so that every
// iteration commits: the same hand pointing at the same artifact twice is
// already true, and §6.3 refuses that work — so a benchmark that repeated one
// reference would be measuring `BenchmarkSignNoOp` and calling it a commit.
//
// The one entry makes the result's list the walk's smallest answer (§9): the
// attested field, with no erasure for a signature commit to report.
func BenchmarkSign(b *testing.B) {
	c, rev := benchSpecSign(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ref := "envelope-a"
		if i%2 == 1 {
			ref = "envelope-b"
		}
		res, err := c.Sign(ctx(), "application.approved_by", rev, ref)
		if err != nil {
			b.Fatalf("Sign: %v", err)
		}
		rev = res.Revision
	}
}

// BenchmarkSignNoOp is the double-click: the same hand, the same content, the
// same reference, answered with success and no commit. The gap to
// `BenchmarkSign` is what the store's writes, the table copy and the reset
// cost — everything an attestation does besides being made, and the reason §8's
// claim that a repeated request is cheap is a measured row rather than a reading
// of the code.
func BenchmarkSignNoOp(b *testing.B) {
	c, rev := benchSpecSign(b)
	res, err := c.Sign(ctx(), "application.approved_by", rev, "envelope")
	if err != nil {
		b.Fatalf("the attestation every timed call repeats: %v", err)
	}
	rev = res.Revision
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		again, err := c.Sign(ctx(), "application.approved_by", rev,
			"envelope")
		if err != nil {
			b.Fatalf("Sign: %v", err)
		}
		if again.Revision != rev {
			b.Fatalf("the repeat committed at %d, want %d", again.Revision,
				rev)
		}
	}
}

// BenchmarkSignRefused is the same call stopped at the door, and the row that
// says what an unauthorized attestation costs a host: the fold, the resolution,
// one mask test and a refusal — before the shape questions, before the revision
// compare, and before either scan that answers what the signature would have
// bound. The actor holds `view`, `edit` and `review` on this form and no `sign`
// grant anywhere in the case, which is the definition §7 ships with.
func BenchmarkSignRefused(b *testing.B) {
	rt := NewRuntime()
	d := &Definition{img: mustLower(b, "bench", specExample(b))}
	if err := rt.Register(d); err != nil {
		b.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "bench", Actor{User: "elliot",
		PrimaryRole: "Registrar"})
	if err != nil {
		b.Fatalf("Create: %v", err)
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := c.Sign(ctx(), "application.approved_by", uint64(i),
			""); err == nil {
			b.Fatal("a registrar with no sign grant attested anyway")
		}
	}
}

// BenchmarkSigReset prices §4.2's invalidation at the seam it actually runs in:
// one diff entry in a form whose attestation stands, measured against the same
// entry in a form whose signature field is empty. Both rows build their table
// the same way inside the loop, so the subtraction is the effect and not the
// fixture — the mapping's two array reads, the standing test, the table copy
// `open` asks for and the write that clears one entry.
//
// It is an effect-level row rather than an operation-level one because that is
// where the rule lives: `Submit` pays it and cannot be timed without it, so the
// three `BenchmarkSign` rows above already carry the erasure's cost on the
// commit path, and this row says how much of that is the new part. A case whose
// forms are unsigned — the state most of a definition's life is spent in —
// answers with no copy at all, which is the second row.
func BenchmarkSigReset(b *testing.B) {
	img := mustLower(b, "bench", specExample(b))
	field := findSlot(img, "application.approved_by")
	name := findSlot(img, "application.full_name")
	changes := []commitChange{{kind: chgValue, slot: name, v: txt("Ada")}}
	// Hoisted out of both loops: the list is the fixture's way of asking one
	// effect, and an iteration that allocated it would be measuring the
	// harness rather than the erasure.
	one := []commitEffect{sigReset{}}

	// `next` is declared outside the loop and assigned within it, which is how
	// the store holds it: `commit` hands the driver the address of a field of
	// its heap-allocated case record, and the snapshot is built there once per
	// commit rather than allocated per iteration of a benchmark.
	var next committedState

	b.Run("standing", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next = committedState{sigs: signedBy(img, field)}
			applyCommitEffects(img, &next, changes, one)
			if sigAt(next.sigs, field).at != 0 {
				b.Fatal("the diff left the attestation standing")
			}
		}
	})
	b.Run("empty", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			next = committedState{sigs: signedBy(img)}
			applyCommitEffects(img, &next, changes, one)
		}
	})
}

// benchTrail is a case with a long past: `commits` whole-form submits, each
// changing the fee so §5.1's diff has something to record. It is the fixture the
// two reads below need and the one `BenchmarkSubmit` deliberately is not — that
// one measures a commit, and a commit's cost cannot be separated from the trail
// it appends to, because the trail grows as the benchmark runs.
//
// The image comes back beside the handle because `BenchmarkReplayTo` folds the
// entries without an operation around them: the derivation is §5.11's internal
// claim, and a host never calls it.
func benchTrail(tb testing.TB, commits int) (Case, *program.Program) {
	tb.Helper()
	rt := NewRuntime()
	img := mustLower(tb, "bench", specExample(tb))
	d := &Definition{img: img}
	if err := rt.Register(d); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), d.ID(),
		Actor{User: "dana", PrimaryRole: "Applicant"})
	if err != nil {
		tb.Fatalf("Create: %v", err)
	}
	base := uint64(0)
	for i := 0; i < commits; i++ {
		res, err := c.Update(ctx(), base, completeApplication(i)...)
		if err != nil {
			tb.Fatalf("Update: %v", err)
		}
		base = res.Working
		if _, err := c.Submit(ctx(), "application", base); err != nil {
			tb.Fatalf("Submit: %v", err)
		}
	}
	return c, img
}

// BenchmarkHistory measures the trail read (RUNTIME §5.11) two ways over the
// same thousand-commit case, and the two rows' difference is the section's cost
// claim: a page costs its page. `page` asks for twenty records from the middle,
// `whole` asks for everything — a thousand records, three entries each — and the
// second is not fifty times the first: the per-record cost of the two is the
// same, and what the small page pays extra is the mask fold and the result it
// carries, which a call does once however long the trail is. Nothing in the read
// searches or copies the trail, so a caller who pages through history pays for
// what they read and nothing else.
//
// It is also the reason §5.11 can say the read does not replay: the entries
// render from their own record, and the fold that answers a question about a
// *state* is the benchmark below, not this one.
func BenchmarkHistory(b *testing.B) {
	c, _ := benchTrail(b, 1000)
	b.Run("page", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.History(ctx(), 500, 20)
			if err != nil {
				b.Fatalf("History: %v", err)
			}
			if len(res.Commits) != 20 || res.Next != 520 {
				b.Fatalf("the page is %d records with next %d",
					len(res.Commits), res.Next)
			}
		}
	})
	b.Run("whole", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.History(ctx(), 0, 0)
			if err != nil {
				b.Fatalf("History: %v", err)
			}
			if len(res.Commits) != 1000 {
				b.Fatalf("the whole trail is %d records", len(res.Commits))
			}
		}
	})
}

// BenchmarkReplayTo measures the derivation the exported read declines to run:
// folding every entry into a state is what "what did this case hold at revision
// 500?" costs, and §5.11's answer is that it is O(trail), not O(page) — which is
// why `History` renders entries from their own records and a host is given the
// trail rather than a snapshot of a past it did not ask to rebuild.
//
// The two rows are the same call at the two ends of the early break, and the
// gap between them is the trail: folding to the head walks the thousand records
// and their three entries each, while folding to revision 1 builds the base
// state and stops at the second record. The second row is therefore what *any*
// derivation costs — a fresh state and its defaults, which is the part a host
// pays even to ask about the case as it stands — and the first is that plus
// every commit the case has ever made.
func BenchmarkReplayTo(b *testing.B) {
	c, img := benchTrail(b, 1000)
	hist, err := c.w.cases.history(ctx(), newRefuser("bench", c.id), 1, 0)
	if err != nil {
		b.Fatalf("history: %v", err)
	}
	if len(hist) != 1000 {
		b.Fatalf("the fixture's trail holds %d records", len(hist))
	}
	// The fee is what the fixture changed on every commit, so reading it out of
	// the derived state says the fold stopped where it was told to: 1500 after
	// the first, 1999 after the thousandth.
	fee := program.NodeID(0)
	for i := range img.SlotCount() {
		if img.Path(program.NodeID(i)) == "application.fee" {
			fee = program.NodeID(i)
		}
	}
	if fee == 0 {
		b.Fatal("the §7 example lost its fee")
	}
	held := func(to uint64) string {
		st := replayTo(img, hist, to)
		c, ok := st.s.cellAt(fee, nil)
		if !ok {
			b.Fatalf("revision %d has no cell for the fee", to)
		}
		return load(img, fee, c.get()).Decimal().String()
	}
	b.Run("head", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := held(1000); got != "1999" {
				b.Fatalf("the head derives a fee of %s, want 1999", got)
			}
		}
	})
	b.Run("first", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := held(1); got != "1500" {
				b.Fatalf("revision 1 derives a fee of %s, want 1500", got)
			}
		}
	})
}
