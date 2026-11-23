package monshi

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
	"strings"
	"testing"

	"github.com/monshihq/monshi/schema"
	"github.com/shopspring/decimal"
)

// parse is the front end, measured separately so §8's "schema → image"
// row is not silently charged the lexer.
func parse(tb testing.TB, src string) *schema.Schema {
	tb.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		tb.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		tb.Fatalf("definition does not compile: %v", s.Errors)
	}
	return s
}

// mustLower is compile() without a *testing.T to insist on, and a TB so a
// test can build the same fixture a benchmark times.
func mustLower(tb testing.TB, id, src string) *image {
	tb.Helper()
	img, err := lower(id, parse(tb, src))
	if err != nil {
		tb.Fatalf("lower: %v", err)
	}
	return img
}

// lowerOnly runs just lowering on a pre-compiled definition.
func lowerOnly(b *testing.B, id string, s *schema.Schema) *image {
	b.Helper()
	img, err := lower(id, s)
	if err != nil {
		b.Fatalf("lower: %v", err)
	}
	return img
}

// BenchmarkLowerSpecExample is DESIGN §3's whole load: text in, image
// out, for the definition the language document is written around.
func BenchmarkLowerSpecExample(b *testing.B) {
	src := specExample(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		img := mustLower(b, "bench", src)
		if len(img.slots) != 20 {
			b.Fatalf("image has %d slots, want the §7 example's 20",
				len(img.slots))
		}
	}
}

// BenchmarkLoweringOnly separates §9's serialization argument from the
// front end it would save: this is the rebuild cost alone.
func BenchmarkLoweringOnly(b *testing.B) {
	s := parse(b, specExample(b))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		img := lowerOnly(b, "bench", s)
		if len(img.conds) < 2 {
			b.Fatalf("image has %d conditions", len(img.conds))
		}
	}
}

// wideSrc is §8's generated corpus: one form, 40 fields, 6 roles, 5
// stages, and one collection whose instances a request would carry. The
// roles carry `view, edit all` rather than view alone so a benchmark on
// this corpus can hold content: an empty frame makes a `View` traversal
// cheaper than the profile §8 describes, and an `Update` on it impossible.
func wideSrc(tb testing.TB) string {
	tb.Helper()
	var w strings.Builder
	w.WriteString("roles {\n")
	for r := 0; r < 6; r++ {
		name := "R" + strconv.Itoa(r)
		w.WriteString("    " + name + " { allow view, edit all; }\n")
	}
	w.WriteString("}\n\nforms {\n    f {\n")
	for i := 0; i < 40; i++ {
		n := strconv.Itoa(i)
		switch i % 4 {
		case 0:
			w.WriteString("        i" + n + " : integer;\n")
		case 1:
			w.WriteString("        s" + n + ` : string;` + "\n")
		case 2:
			w.WriteString("        d" + n +
				" : decimal(12,2) default 1.00;\n")
		default:
			w.WriteString("        b" + n + " : boolean;\n")
		}
	}
	w.WriteString("        rows multiple {\n")
	for i := 0; i < 8; i++ {
		w.WriteString("            c" + strconv.Itoa(i) + " : integer;\n")
	}
	w.WriteString("        }\n    }\n}\n\nstages {\n")
	for s := 0; s < 5; s++ {
		name := "s" + strconv.Itoa(s)
		w.WriteString("    " + name + " {\n")
		w.WriteString("        allow R" + strconv.Itoa(s) + " submit f;\n")
		if s < 4 {
			w.WriteString("        accept s" + strconv.Itoa(s+1) + ";\n")
		}
		w.WriteString("    }\n")
	}
	w.WriteString("}\n")
	return w.String()
}

func BenchmarkLoweringWide(b *testing.B) {
	src := wideSrc(b)
	b.ReportAllocs()
	var slots int
	for i := 0; i < b.N; i++ {
		img := mustLower(b, "wide", src)
		slots = len(img.slots)
	}
	b.ReportMetric(float64(slots), "slots/op")
}

// BenchmarkLoweringWideOnly is §9's serialization argument on the corpus
// it describes: the 51-slot image rebuilds in microseconds, so shipping
// images instead of source buys nothing measurable.
func BenchmarkLoweringWideOnly(b *testing.B) {
	s := parse(b, wideSrc(b))
	b.ReportAllocs()
	var slots int
	for i := 0; i < b.N; i++ {
		slots = len(lowerOnly(b, "wide", s).slots)
	}
	b.ReportMetric(float64(slots), "slots/op")
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
	c := img.cond(img.slots[internal].condPres)
	if c == nil {
		b.Fatal("application.internal lost its presence guard")
	}
	view := make(stateView, len(img.slots))
	view[kind] = slotValue{set: true,
		i: int64(img.types[img.slots[kind].typ].tags[1])} // COMPANY
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
	img := mustLower(b, "bench", wideSrc(b))
	role := findRole(img, "R1")
	stage := findStage(img, "s1")
	roles := []roleID{role} // the actor's set is a request fact, not a loop body
	n := len(img.slots)
	var held uint8
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		held = 0
		for s := range img.slots {
			held |= img.effective(roles, stage, nodeID(s))
		}
	}
	if held&permView == 0 {
		b.Fatal("R1 holds no view bits")
	}
	// One op is the whole walk, so the per-slot figure is this loop's
	// cost divided by the slots it touched.
	b.StopTimer()
	b.ReportMetric(float64(n), "slots/op")
}

// --- lookups by name, for fixtures only: a benchmark states its target as a
// path, so a corpus change is obvious in the failure rather than silent in the
// number. ---

func findSlot(img *image, path string) nodeID {
	for i := range img.slots {
		if img.path(nodeID(i)) == path {
			return nodeID(i)
		}
	}
	panic("no slot at " + path)
}

func findRole(img *image, name string) roleID {
	for i := range img.roleIdx {
		if img.roleIdx[i] != 0 && img.name(img.roleIdx[i]) == name {
			return roleID(i)
		}
	}
	panic("no role " + name)
}

func findStage(img *image, name string) stageID {
	for i := range img.stages {
		if img.stages[i].name != 0 && img.name(img.stages[i].name) == name {
			return stageID(i)
		}
	}
	panic("no stage " + name)
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
	wide := mustLower(b, "wide", wideSrc(b))
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
func walkOne(b *testing.B, img *image, wrote nodeID) {
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
func walkAll(b *testing.B, img *image) {
	b.Helper()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		f := newFanout(img)
		for n := range img.slots {
			if img.slots[n].coll != 0 {
				f.wroteRows(nodeID(n))
			} else {
				f.wroteField(nodeID(n))
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
	d := &Definition{img: mustLower(tb, "wide", wideSrc(tb))}
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
		slots := len(mustLower(b, "wide", wideSrc(b)).slots)
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
