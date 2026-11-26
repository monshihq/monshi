package engine

// The cost of the exported hook seam.
//
// Three shapes per operation. No binding at all: the registry is consulted and
// answers absent, which is the price every assignment in this package has paid
// since `Validate` existed — an unbound custom type is identity, and identity
// arrives here as a map miss rather than a branch. One normalizer that returns
// what it was handed: the floor no host rule can go below, and the cost of the
// call itself. And the same normalizer with one masked read first, which is
// what `CaseContext` adds to the middle row — a snapshot beside the call, and
// the mask test in front of an answer.
//
// Compare the same hook with and without one case read. View already has a
// visibility mask, while Update scans the node's subtree for visible grants.
//
// `BenchmarkCaseContextReads` prices the snapshot's reads apart from any
// operation, and on two corpora for that reason: the wide one, where the role
// is granted everything so the two mask branches do the same work, and
// `blindHookSrc`, where one form is granted and another is not — the only
// shape on which a scan may cost more than a fold, and the one that gives the
// scan its bound.
//
// The corpus is the wide one with its ten decimal fields declared through a
// custom type, because a binding is keyed by a type name and a definition of
// built-ins alone gives a host nothing to bind to.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// wideHookSrc is `wideSrc` with its ten decimal fields declared through the
// custom type `q`, which is an alias in every respect the engine
// distinguishes: the same class, the same bounds, the same default.
func wideHookSrc(tb testing.TB) string {
	tb.Helper()
	var w strings.Builder
	w.WriteString("types {\n    q : decimal(12,2);\n}\n\n")
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
			w.WriteString("        d" + n + " : q default 1.00;\n")
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

// benchHookCase is `benchWideCase` on the bindable corpus, and hands back the
// runtime as well as the case: registering after the case exists is the order
// that proves the registry is the runtime's and not a table a handle copied.
// The five bound fields it fills are the work the seam is asked to price.
func benchHookCase(tb testing.TB, rows int) (*Runtime, Case, uint64) {
	tb.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(tb, "hooked", wideHookSrc(tb))}
	if err := rt.Register(d); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "hooked",
		Actor{User: "dana", PrimaryRole: "R1"})
	if err != nil {
		tb.Fatalf("Create: %v", err)
	}
	edits := hookEdits()
	edits = append(edits,
		Set("f.i0", Int(1)),
		Set("f.s1", Text("Monshi Ltd")),
		Set("f.b3", Bool(true)))
	for i := 0; i < rows; i++ {
		edits = append(edits, Add("f.rows"))
	}
	res, err := c.Update(ctx(), 0, edits...)
	if err != nil {
		tb.Fatalf("Update: %v", err)
	}
	if _, err := c.View(ctx()); err != nil {
		tb.Fatalf("View: %v", err)
	}
	return rt, c, res.Working
}

// hookEdits is the batch every `update/*` row runs: five assignments of the
// bound custom type, all of them the same value, so the difference between
// rows is what the engine does with a key and not what a host does with data.
func hookEdits() []Edit {
	v := Decimal(decimal.NewFromInt(1500))
	return []Edit{
		Set("f.d2", v), Set("f.d6", v), Set("f.d10", v),
		Set("f.d14", v), Set("f.d18", v),
	}
}

// identity is the cheapest rule a host can write: the value it was handed,
// back again. It is bound to `q`, so every assignment of one of the corpus's
// ten decimal fields goes through it, and every one of those calls does the
// least work the contract allows — which is why its row is a floor and not a
// sample of host code.
func identity() ValidateFunc {
	return func(_ context.Context, _ CaseContext, _ string,
		v Value) (Value, error) {
		return v, nil
	}
}

// blindHookSrc is the corpus the mask's two branches are *not* obliged to
// answer alike: the actor's role is granted one form and refused the other,
// which is the case the mask rule is really about. The visible form
// carries one field of the bound type, so a host rule runs; the hidden one
// carries forty fields, so the preorder span a scan must walk to answer
// "nothing here is visible" is wide.
func blindHookSrc() string {
	var w strings.Builder
	w.WriteString("types {\n    q : decimal(12,2);\n}\n\n")
	w.WriteString("roles {\n")
	w.WriteString("    R1 { allow view, edit f; }\n")
	w.WriteString("    R2 { allow view, edit g; }\n")
	w.WriteString("}\n\nforms {\n    f {\n        b : q default 1.00;\n    }\n")
	w.WriteString("    g {\n")
	w.WriteString("        c0 : integer default 7;\n")
	for i := 1; i < 40; i++ {
		w.WriteString("        c" + strconv.Itoa(i) + " : integer;\n")
	}
	w.WriteString("    }\n}\n\nstages {\n    s0 {\n")
	w.WriteString("        allow R1 submit f;\n        accept s1;\n    }\n")
	w.WriteString("    s1 {\n        allow R2 submit g;\n    }\n}\n")
	return w.String()
}

// pinBlind hands back a snapshot pinned on the blind corpus by a real
// operation: through a `View`, which holds the caller's fold, or through a
// one-write `Update`, which holds none and answers reads by the range.
func pinBlind(tb testing.TB, viaView bool) CaseContext {
	tb.Helper()
	rt := NewRuntime()
	d := &Definition{img: mustLower(tb, "blind", blindHookSrc())}
	if err := rt.Register(d); err != nil {
		tb.Fatalf("Register: %v", err)
	}
	c, err := rt.Create(ctx(), "blind", Actor{User: "dana", PrimaryRole: "R1"})
	if err != nil {
		tb.Fatalf("Create: %v", err)
	}
	var at CaseContext
	rt.RegisterValidate("q", func(_ context.Context, x CaseContext,
		_ string, v Value) (Value, error) {
		at = x
		return v, nil
	})
	var err2 error
	if viaView {
		_, err2 = c.View(ctx())
	} else {
		_, err2 = c.Update(ctx(), 0,
			Set("f.b", Decimal(decimal.NewFromInt(15))))
	}
	if err2 != nil {
		tb.Fatalf("pinning a blind snapshot: %v", err2)
	}
	if _, ok := at.Value("f.b"); !ok {
		tb.Fatal("the granted form's own field is not readable")
	}
	if _, ok := at.Value("g.c0"); ok {
		tb.Fatal("a withheld form answered a read")
	}
	return at
}

func BenchmarkHookSeam(b *testing.B) {
	b.Run("update/no_binding", func(b *testing.B) {
		_, c, base := benchHookCase(b, 4)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.Update(ctx(), base, hookEdits()...)
			if err != nil {
				b.Fatalf("Update: %v", err)
			}
			base = res.Working
		}
	})

	b.Run("update/bound", func(b *testing.B) {
		rt, c, base := benchHookCase(b, 4)
		rt.RegisterValidate("q", identity())
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.Update(ctx(), base, hookEdits()...)
			if err != nil {
				b.Fatalf("Update: %v", err)
			}
			base = res.Working
		}
	})

	// The snapshot is built once per batch and not once per call, so this
	// row's gap over the last is the read: a path resolution, the node's
	// range scanned for the caller's `view`, and one cell loaded.
	b.Run("update/bound_read", func(b *testing.B) {
		rt, c, base := benchHookCase(b, 4)
		rt.RegisterValidate("q", func(_ context.Context, at CaseContext,
			_ string, v Value) (Value, error) {
			_, _ = at.Value("f.i0")
			return v, nil
		})
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			res, err := c.Update(ctx(), base, hookEdits()...)
			if err != nil {
				b.Fatalf("Update: %v", err)
			}
			base = res.Working
		}
	})

	// The same corpus read whole, with no host function on its types at all.
	b.Run("view/no_binding", func(b *testing.B) {
		_, c, _ := benchHookCase(b, 4)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := c.View(ctx()); err != nil {
				b.Fatalf("View: %v", err)
			}
		}
	})

	// The second reading: a view asks the same normalizer of every stored value
	// it renders, so all ten decimal fields are ten calls in the read — five
	// because the batch wrote them and five because their declared default is a
	// value — and the fixed-point check runs on each.
	b.Run("view/bound", func(b *testing.B) {
		rt, c, _ := benchHookCase(b, 4)
		rt.RegisterValidate("q", identity())
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := c.View(ctx()); err != nil {
				b.Fatalf("View: %v", err)
			}
		}
	})

	// The fold branch of the mask, priced where it is paid: the view holds
	// the array already, so a read is a lookup rather than a scan.
	b.Run("view/bound_read", func(b *testing.B) {
		rt, c, _ := benchHookCase(b, 4)
		rt.RegisterValidate("q", func(_ context.Context, at CaseContext,
			_ string, v Value) (Value, error) {
			_, _ = at.Value("f.i0")
			return v, nil
		})
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := c.View(ctx()); err != nil {
				b.Fatalf("View: %v", err)
			}
		}
	})
}

// readSink keeps the isolated rows honest. A loop that asks a read for its
// `ok` flag alone lets the compiler drop the part of an inlined read that
// builds the answer, and the row would then measure the mask without the
// value. The sinks are written through a package-level variable, so every row
// below pays for the conversion it is claiming to price.
var (
	readSink Value
	nameSink []string
)

// BenchmarkCaseContextReads isolates the snapshot's own reads from the
// operations that hand it out, by keeping a snapshot a real batch pinned and
// calling it the way a host's rule does. The two `value` rows are the mask's
// two branches on one path.
func BenchmarkCaseContextReads(b *testing.B) {
	pin := func(viaView bool) CaseContext {
		rt, c2, base := benchHookCase(b, 4)
		var at CaseContext
		rt.RegisterValidate("q", func(_ context.Context, x CaseContext,
			_ string, v Value) (Value, error) {
			at = x
			return v, nil
		})
		var err error
		if viaView {
			_, err = c2.View(ctx())
		} else {
			_, err = c2.Update(ctx(), base, hookEdits()...)
		}
		if err != nil {
			b.Fatalf("pinning a snapshot: %v", err)
		}
		return at
	}

	b.Run("value_fold", func(b *testing.B) {
		at := pin(true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v, ok := at.Value("f.i0")
			if !ok {
				b.Fatal("the fold answered nothing for a visible field")
			}
			readSink = v
		}
		if readSink.Kind() == KindNone {
			b.Fatal("the sunk read answered nothing")
		}
	})

	b.Run("value_range", func(b *testing.B) {
		at := pin(false)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			v, ok := at.Value("f.i0")
			if !ok {
				b.Fatal("the range answered nothing for a visible field")
			}
			readSink = v
		}
	})

	// A path no declaration is reached by: the resolution fails, and the
	// mask is never asked. This is the branch a host hits when its own
	// vocabulary is wrong, and it is the cheapest read the type has.
	b.Run("value_miss", func(b *testing.B) {
		at := pin(true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Value("f.not_a_field"); ok {
				b.Fatal("a path outside the definition answered")
			}
		}
	})

	// The unit's decision beside a field: a slot read, one table lookup at
	// the governing unit, and the same mask as the value's. On this corpus
	// no unit governs, so the answer is absence — the cheapest honest thing
	// a verdict read can do, and the row that says what asking costs.
	b.Run("review", func(b *testing.B) {
		at := pin(true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Review("f.d2"); ok {
				b.Fatal("the corpus declares no verifiable unit")
			}
		}
	})

	// The shape on which the two mask rules may cost differently rather than
	// the same: a container's verdict passes through the mask before the image
	// is asked whether a unit governs, so the range branch walks the node's
	// preorder span until it finds a descendant the caller may see — the
	// whole span when nobody may — where the fold reads one array slot. On
	// this corpus the role is granted everything, so the scan stops at the
	// node itself and the two rows agree; `blindHookSrc` is what prices a
	// withheld subtree, in the `hidden_*` rows below. A *value* read never
	// reaches the mask for a container at all: `hasValue` is tested first,
	// which refuses the question more cheaply than answering it would.
	b.Run("review_container_fold", func(b *testing.B) {
		at := pin(true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Review("f"); ok {
				b.Fatal("a form is not a unit on this corpus")
			}
		}
	})
	b.Run("review_container_range", func(b *testing.B) {
		at := pin(false)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Review("f"); ok {
				b.Fatal("a form is not a unit on this corpus")
			}
		}
	})

	// Both paths must look absent to this actor, even though one exists outside
	// the actor's visible subtree.
	b.Run("hidden_value_fold", func(b *testing.B) {
		at := pinBlind(b, true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Value("g.c0"); ok {
				b.Fatal("a withheld leaf answered")
			}
		}
	})
	b.Run("hidden_value_range", func(b *testing.B) {
		at := pinBlind(b, false)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Value("g.c0"); ok {
				b.Fatal("a withheld leaf answered")
			}
		}
	})

	// And the worst case the scan has: a withheld *container*, whose span is
	// forty-one nodes and holds nothing the caller may see, so the walk runs
	// to its end before it can say so. This is the row that bounds what a
	// host is allowed to cost a reader for asking about a subtree it was
	// pruned from, and it is bounded by declaration breadth rather than by
	// the case's data.
	b.Run("hidden_container_fold", func(b *testing.B) {
		at := pinBlind(b, true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Review("g"); ok {
				b.Fatal("a withheld form answered a verdict")
			}
		}
	})
	b.Run("hidden_container_range", func(b *testing.B) {
		at := pinBlind(b, false)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, ok := at.Review("g"); ok {
				b.Fatal("a withheld form answered a verdict")
			}
		}
	})

	// Every role name the actor holds, folded from the committed state. It
	// allocates because the answer is a list the host keeps after the call;
	// the cost claim is about the reads that hold nothing new.
	b.Run("roles", func(b *testing.B) {
		at := pin(true)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			names := at.Roles()
			if len(names) != 1 {
				b.Fatal("the role list is not the one name held")
			}
			nameSink = names
		}
		if len(nameSink) != 1 {
			b.Fatal("the sunk role list is not one name")
		}
	})
}

// commitSink keeps an observer's body from being dead code. A host's real
// fan-out does I/O; the row here is the engine's share of the cost — the
// folds, the render and the snapshot — with the cheapest possible body.
var commitSink int

// BenchmarkCommitNotify prices the dispatch on the operation that pays it
// most: a submit, whose commit also runs the effects, and whose observer
// is handed the widest record. The flow inside each iteration is one edit
// and one submit, so every row after `none` differs from it by exactly the
// notification — and `none` is the claim that an unwired host pays one nil
// slice read for a seam it does not use.
func BenchmarkCommitNotify(b *testing.B) {
	shapes := []struct {
		name string
		n    int
	}{
		{"none", 0}, {"one", 1}, {"three", 3},
	}
	for _, sh := range shapes {
		b.Run(sh.name, func(b *testing.B) {
			rt := NewRuntime()
			d := &Definition{img: mustLower(b, "notified", wideHookSrc(b))}
			if err := rt.Register(d); err != nil {
				b.Fatalf("Register: %v", err)
			}
			// R0 because stage s0 is the one that grants it the submit.
			c, err := rt.Create(ctx(), "notified",
				Actor{User: "dana", PrimaryRole: "R0"})
			if err != nil {
				b.Fatalf("Create: %v", err)
			}
			res, err := c.Update(ctx(), 0, hookEdits()...)
			if err != nil {
				b.Fatalf("Update: %v", err)
			}
			work, rev := res.Working, uint64(0)
			for i := 0; i < sh.n; i++ {
				rt.RegisterOnCommit(func(context.Context, CaseContext,
					Commit) {
					commitSink++
				})
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				up, err := c.Update(ctx(), work, Set("f.i0", Int(int64(i))))
				if err != nil {
					b.Fatalf("Update: %v", err)
				}
				work = up.Working
				sub, err := c.Submit(ctx(), "f", work)
				if err != nil {
					b.Fatalf("Submit: %v", err)
				}
				if sub.Revision != rev+uint64(i)+1 {
					b.Fatalf("revision = %d, want %d", sub.Revision,
						rev+uint64(i)+1)
				}
			}
		})
	}
}
