package monshi

// The fanout's mechanics (RUNTIME §5.3), asserted where the marks and the queue
// are visible. What `affected_test.go` covers is the returned list; what is here
// is the walk that produced it, and the four claims a client is relying on
// without knowing it: that nothing written escapes the answer, that nothing in
// the answer was not reached, that the frontier stays bounded by what lowering
// promised, and that the set does not depend on what the batch actually stored.

import (
	"context"
	"strings"
	"testing"
)

// shape checks one fanout result against the marks it came from.
//
//   - every reported path is a marked slot, so the answer is a rendering of the
//     walk rather than of anything else;
//   - every marked slot is either reported or inside a reported subtree, which
//     is the soundness direction a stale screen depends on;
//   - declaration order, and no path containing another;
//   - the queue never grew past `depBound`, which is what §5.3 promises before
//     any request knows what it is about to write.
func shape(t *testing.T, img *image, f *fanout, got []string) {
	t.Helper()

	at := map[string]nodeID{}
	for n := range img.slots {
		at[img.path(nodeID(n))] = nodeID(n)
	}
	prev := nodeID(0)
	for i, p := range got {
		n, ok := at[p]
		if !ok {
			t.Fatalf("%q is reported but is no slot of this definition", p)
		}
		if i > 0 && n <= prev {
			t.Errorf("%q follows a later declaration: the list is not in "+
				"declaration order", p)
		}
		prev = n
		for _, q := range got[i+1:] {
			if strings.HasPrefix(q, p+".") {
				t.Errorf("%q contains %q: a group must stand for its subtree",
					p, q)
			}
		}
	}

	for n := range f.mark {
		if !f.mark[n] {
			continue
		}
		p := img.path(nodeID(n))
		covered := false
		for _, q := range got {
			if q == p || strings.HasPrefix(p, q+".") {
				covered = true
				break
			}
		}
		if !covered {
			t.Errorf("slot %q is marked and reported by no path, so a client "+
				"refreshing the answer keeps a view the walk changed", p)
		}
	}
	if len(f.queue) > int(img.depBound) {
		t.Errorf("the walk enqueued %d slots against a bound of %d",
			len(f.queue), img.depBound)
	}
}

// TestEveryWriteIsCoveredBySomeReportedPath runs the walk once per declaration
// in the SYNTAX §7 example, which is the corpus §8 measures and the one whose
// guards, stages and collections are already known to be real. The claim is the
// one that cannot be made from outside: not that a particular batch reports
// particular paths, but that no single write anywhere in a definition can leave
// a marked slot unreported — the property a hand-written list of cases argues
// about one path at a time and never proves.
func TestEveryWriteIsCoveredBySomeReportedPath(t *testing.T) {
	img := lowerExample(t)
	for n := range img.slots {
		id := nodeID(n)
		f := newFanout(img)
		// A collection is written by an instance arriving or leaving, which is
		// what §5.1's `Add` does to it; every other slot is a value.
		if img.slots[n].coll != 0 {
			f.wroteRows(id)
		} else {
			f.wroteField(id)
		}
		got := f.affected()
		if len(got) == 0 {
			t.Errorf("writing %q reports nothing at all", img.path(id))
			continue
		}
		shape(t, img, f, got)
	}
}

// TestTheClosureReachesThroughASubtree is why the walk expands a re-asked owner
// to its subtree instead of stopping at the owner's own path. `district` reads a
// *member* of `region`, so an edit to `province` reaches it only if the slots
// under an opened group are queued as well as marked: report the group, and the
// field outside it that the group's visibility decides stays stale on screen.
//
// The third level is there because the shape of the argument does not change at
// depth two. A walk that queues only the owner passes three levels of this and
// fails the fourth.
func TestTheClosureReachesThroughASubtree(t *testing.T) {
	img := mustLowerSource(t, `
roles {
    Applicant { allow view, edit application; }
}
forms {
    application {
        a : string;
        b present[.a == "1"] {
            c : string;
        }
        d present[.b.c == "2"] {
            e : string;
        }
        tail : string present[.d.e == "3"];
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`)
	f := newFanout(img)
	f.wroteField(slotByPath(t, img, "application.a"))
	got := f.affected()

	want := []string{"application.a", "application.b", "application.d",
		"application.tail"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("one write reached %v, want %v: the closure stops short of a "+
			"field that reads inside an opened subtree", got, want)
	}
	shape(t, img, f, got)

	// `c` and `e` are marked, and reported by their groups rather than by
	// name — the collapse, seen from the marks rather than the paths.
	for _, p := range []string{"application.b.c", "application.d.e"} {
		if !f.mark[slotByPath(t, img, p)] {
			t.Errorf("%s is not marked, so nothing under it was re-asked", p)
		}
		for _, q := range got {
			if q == p {
				t.Errorf("%s is named beside the group that contains it", p)
			}
		}
	}
}

// TestADiamondMarksASharedDependentOnce is the closure's other shape: two paths
// to one declaration. `guard` reads both fields a batch writes, so it is a
// dependent of two marks at once, and a walk without the visited set would
// enqueue it twice — which is not merely wasteful, because the queue's bound is
// `depBound` and a slot counted twice breaks the promise §5.3 makes about it.
func TestADiamondMarksASharedDependentOnce(t *testing.T) {
	img := mustLowerSource(t, `
roles {
    Applicant { allow view, edit application; }
}
forms {
    application {
        left  : string;
        right : string;
        both  : string present[.left == .right];
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`)
	f := newFanout(img)
	f.wroteField(slotByPath(t, img, "application.left"))
	f.wroteField(slotByPath(t, img, "application.right"))
	got := f.affected()

	if want := "application.left application.right application.both"; strings.
		Join(got, " ") != want {
		t.Fatalf("got %v, want [%s]", got, want)
	}
	shared := slotByPath(t, img, "application.both")
	hits := 0
	for _, q := range f.queue {
		if q == shared {
			hits++
		}
	}
	if hits != 1 {
		t.Errorf("the shared dependent was enqueued %d times, want once", hits)
	}
	shape(t, img, f, got)
}

// TestAGuardInsideACollectionReasksItsCollection is the third rule, and the
// language is what makes it right rather than merely tidy. F6 says a condition
// inside a `multiple` declaration applies uniformly to the whole collection, so
// when `kind` moves it is the *group's* answer that moved: a client told about
// `holders.since` would refresh one column of rows whose requiredness all
// changed at once.
//
// E2 is the other half — the reason the collection is enough and not merely
// neater. A guard whose operand traverses a `multiple` node does not compile, so
// no slot inside a collection's rows is ever an adjacency key, and there is
// nothing beyond the group to reach. That is asserted here rather than trusted,
// because it is the fact the rule is priced by: if E2 ever admits a row operand,
// this test says so instead of the report quietly getting wrong.
func TestAGuardInsideACollectionReasksItsCollection(t *testing.T) {
	img := mustLowerSource(t, `
roles {
    Applicant { allow view, edit application; }
}
forms {
    application {
        kind : enum(PERSON, COMPANY);
        holders multiple {
            who   : string;
            since : date required[.kind == PERSON];
        }
    }
}
stages {
    draft { allow Applicant accept; accept filed; }
    filed { }
}
`)
	for n := range img.slots {
		id := nodeID(n)
		if img.collOf(id) != 0 && img.depFrom[n].n != 0 {
			t.Fatalf("%s lives in a collection's rows and is an adjacency key "+
				"with %d owners, so E2 no longer says what this rule assumes",
				img.path(id), img.depFrom[n].n)
		}
	}

	f := newFanout(img)
	f.wroteField(slotByPath(t, img, "application.kind"))
	got := f.affected()
	if want := "application.kind application.holders"; strings.Join(got,
		" ") != want {
		t.Fatalf("got %v, want [%s]: the uniform answer belongs to the "+
			"collection, not to the member that spells it", got, want)
	}
	shape(t, img, f, got)

	// Marked, because its guard was re-asked; unnamed, because the group stands
	// for it. Both halves matter: the first is what a §5.4 walk would use, and
	// the second is what a client can prefix-match.
	if !f.mark[slotByPath(t, img, "application.holders.since")] {
		t.Error("the member whose condition moved is not marked")
	}
}

// TestAFanoutDoesNotSeeWhatWasWritten states the property that makes the walk
// cheap enough to run per batch: it reads the adjacency and nothing else, so
// which slots a batch touched decides the answer and what landed in them does
// not. That is what lets DESIGN §7 re-evaluate dependencies after `Validate`
// normalization without re-running the closure — the marks name the rows
// holding normalized values, and whoever *asks* a guard asks the state the save
// published.
//
// The hook below is the test's real content. It rewrites the tag the host asked
// for, so the draft's presence answer differs between the two batches while
// their affected sets must not: a report that moved with the value would mean
// the walk consulted data, and data is what §5.2 derives at read time.
func TestAFanoutDoesNotSeeWhatWasWritten(t *testing.T) {
	rt, img, c := editCase(t, danaEdit)
	sub := slotByPath(t, img, "application.internal")

	// one writes the tag and reports both halves of the answer: what the batch
	// said had changed, and what the draft now derives. The revision is threaded
	// because every call below is a real batch on one working copy.
	base := uint64(0)
	one := func(want string) (string, bool) {
		t.Helper()
		res, err := c.Update(ctx(), base, Set("application.kind", Tag(want)))
		if err != nil {
			t.Fatalf("Update(%s): %v", want, err)
		}
		base = res.Working
		s := draftState(t, c, img)
		return strings.Join(res.Affected, " "), s.reached(sub)
	}

	// Without a hook: the two tags give opposite presence answers and the same
	// report, which is the property under test stated where nothing else can
	// interfere.
	free, opened := one("COMPANY")
	same, closed := one("PERSON")
	if opened == closed {
		t.Fatalf("the fixture's guard does not decide `internal`'s presence, "+
			"so the comparison below compares nothing: %v then %v", opened,
			closed)
	}
	if free != same {
		t.Fatalf("the report moved with the value, from [%s] to [%s]: the walk "+
			"consulted data it is not allowed to read", free, same)
	}

	// With a hook, on a batch that asked for the tag which *closes* the subtree:
	// the report is still identical, because the mark is the slot the
	// normalizer wrote into rather than the value it put there — while the
	// *answer* the draft derives is now the hook's, which is the other half of
	// DESIGN §7's ordering. A client that refreshed `internal` because the
	// engine named it is refreshing a subtree the normalization opened, and
	// nothing but the report could have told it to look.
	rt.RegisterValidate("legal", func(_ context.Context, _ CaseContext,
		_ string, _ Value) (Value, error) {
		return Tag("COMPANY"), nil
	})
	gated, forced := one("PERSON")
	if gated != free {
		t.Errorf("a normalizer changed the report from [%s] to [%s]: the walk "+
			"must mark slots, not values", free, gated)
	}
	if !forced {
		t.Error("the hook's tag did not reach the guard, so the closure ran " +
			"against a state the save never published")
	}
}
