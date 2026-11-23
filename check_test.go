package monshi

// The §5.4 completeness walk, asserted where the walk itself is visible.
//
// `submit_test.go` covers what a host can observe about a refused submit.
// What is here is the five claims only this package can make: that an
// unreachable subtree is *not asked* rather than asked and excused, that a
// required collection is asked once per place its rows live, that a member
// is asked once per instance of the collection that holds it, that a member
// of a collection nobody filled in is not reported at all, and that every
// address the walk produces resolves back to the cell it came from.
//
// The last is the one that turns a report into something a client can act
// on, and it is the one a change to the numbering would break silently —
// §5.1's instance IDs and §3.2's preorder are both load-bearing for it, and
// neither is visible from a refusal's text alone.

import (
	"strings"
	"testing"
)

// checkSrc states each claim with one declaration: a required field, a
// gated subtree with a required member, and a required collection with a
// required member and a required collection *inside* it. Role A owns the
// form, so permissions are not what any test here is about.
const checkSrc = `
roles {
    A { allow view, edit, submit f; }
}
forms {
    f {
        name : string required;
        gate : string;

        deep present[.gate == "y"] {
            risk : integer required;
        }

        holders multiple required {
            who : string required;

            homes multiple required {
                city : string required;
            }
        }
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`

// gapStrings renders a walk's result as the list a host would be shown, so a
// test can say what is in it rather than what its entries' fields are.
func gapStrings(gs []gap) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.path)
	}
	return out
}

// wantGaps fails unless the walk reported exactly these paths, in this
// order. Order is part of the claim, not decoration: the list is what a form
// renders top to bottom, and it is grouped by *declaration* rather than by
// row because the walk is one index loop over the preorder range.
func wantGaps(t *testing.T, got []gap, want ...string) {
	t.Helper()
	list := gapStrings(got)
	if len(list) != len(want) {
		t.Fatalf("gaps = %q, want %q", list, want)
	}
	for i := range want {
		if list[i] != want[i] {
			t.Errorf("gap %d = %q, want %q (whole list %q)", i, list[i],
				want[i], list)
		}
	}
}

// write is this file's way of storing one text value: the walk is a read, so
// these tests set up states directly rather than routing through an operation
// that has its own claims to test. `authority_test.go` has the same helper
// taking a payload; this one takes the text these declarations all hold.
func write(t *testing.T, s *state, path, text string) {
	t.Helper()
	r, ok := s.resolve(path)
	if !ok {
		t.Fatalf("%q resolves to nothing", path)
	}
	if !r.hasValue {
		t.Fatalf("%q reaches no value", path)
	}
	r.cell.set(slotValue{set: true, s: text})
}

func TestIncompleteAsksOnlyWhatIsRequired(t *testing.T) {
	img := mustLowerSource(t, checkSrc)
	s := newState(img)
	f := slotByPath(t, img, "f")

	// Nothing entered: the required field, and the required collection with
	// no instances. `gate` is not reported because nothing requires it, and
	// `deep.risk` is not reported because its group is not part of the
	// effective state — F5's licence, and the difference between a form that
	// asks for an answer and one that asks for an answer to a question its
	// own definition withdrew.
	wantGaps(t, s.incomplete(f), "f.name", "f.holders")

	// Open the gate: the subtree is now asked about, in preorder, between the
	// field before it and the collection after it.
	write(t, &s, "f.gate", "y")
	wantGaps(t, s.incomplete(f), "f.name", "f.deep.risk", "f.holders")

	// A filled field stops being reported, and one that was filled and then
	// cleared starts again: §5.2's emptiness-not-existence, seen through the
	// walk that has to render it. Presence is untouched either way — the
	// cleared field is still part of the structure.
	write(t, &s, "f.name", "Monshi Ltd")
	wantGaps(t, s.incomplete(f), "f.deep.risk", "f.holders")
	if c, ok := s.cellOf(slotByPath(t, img, "f.name")); !ok {
		t.Fatal("f.name has no cell")
	} else {
		c.clear()
	}
	wantGaps(t, s.incomplete(f), "f.name", "f.deep.risk", "f.holders")
}

func TestIncompleteAsksEveryInstance(t *testing.T) {
	img := mustLowerSource(t, checkSrc)
	s := newState(img)
	f := slotByPath(t, img, "f")
	holders := slotByPath(t, img, "f.holders")

	// Two instances, neither filled. The collection itself stops being
	// reported once it has rows, and its member is reported once per row —
	// asking "for the declaration" would report one gap where two rows are
	// short, and the second would reach a committed case with nothing in it.
	for i := 0; i < 2; i++ {
		if _, _, ok := s.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	wantGaps(t, s.incomplete(f), "f.name",
		"f.holders.1.who", "f.holders.2.who",
		"f.holders.1.homes", "f.holders.2.homes")

	// Filling one row's member leaves the other's reported, at its own
	// address. This is the per-instance half of §5.2's second line, and the
	// reason a completeness report cannot be a list of declarations.
	write(t, &s, "f.holders.1.who", "Ada")
	wantGaps(t, s.incomplete(f),
		"f.name", "f.holders.2.who",
		"f.holders.1.homes", "f.holders.2.homes")
}

func TestIncompleteAsksANestedCollectionPerOuterInstance(t *testing.T) {
	img := mustLowerSource(t, checkSrc)
	s := newState(img)
	f := slotByPath(t, img, "f")
	holders := slotByPath(t, img, "f.holders")
	homes := slotByPath(t, img, "f.holders.homes")

	for i := 0; i < 2; i++ {
		if _, _, ok := s.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	// `homes` is asked at its holder's address, because that is where its
	// rows live. Answering it from the set the state holds at the top level
	// would ask a collection that has no rows there and report nothing about
	// the two places that need them.
	got := s.incomplete(f)
	wantGaps(t, got, "f.name",
		"f.holders.1.who", "f.holders.2.who",
		"f.holders.1.homes", "f.holders.2.homes")

	// A member two collections deep is not reported while *its* collection
	// has no rows: there is no place to put the value, so there is no answer
	// to ask for (F5's rule one level further out). Adding a home per holder
	// makes `city` real, and its address carries both instance IDs — the
	// reason §5.1 requires stable IDs rather than ordinals.
	for _, id := range []instanceID{1, 2} {
		if _, _, ok := s.addAt(homes, instPath{{holders, id}}); !ok {
			t.Fatalf("Add refused the nested collection at holder %d", id)
		}
	}
	wantGaps(t, s.incomplete(f), "f.name",
		"f.holders.1.who", "f.holders.2.who",
		"f.holders.1.homes.3.city", "f.holders.2.homes.4.city")
}

func TestIncompleteAsksARepeatedFieldPerHost(t *testing.T) {
	// A repeated *field* is the one collection whose row set is not the
	// answer to "does this have instances": its own rows hold its values, so
	// the place to ask is the host that holds the set. Asking the set itself
	// reports nothing while a required repeated field is unfilled, which is
	// §5.4's completeness question failing to be asked at all.
	const src = `
roles {
    A { allow view, edit, submit f; }
}
forms {
    f {
        holders multiple {
            notes : string multiple required;
        }
    }
}
stages {
    start { allow A view, edit, submit f; }
}
`
	img := mustLowerSource(t, src)
	s := newState(img)
	f := slotByPath(t, img, "f")
	holders := slotByPath(t, img, "f.holders")
	notes := slotByPath(t, img, "f.holders.notes")

	// No holders: nothing to ask, because a required member of a collection
	// that has no rows has no place to hold a value (F5).
	if got := s.incomplete(f); len(got) != 0 {
		t.Fatalf("gaps = %q, want none with no instances to ask about",
			gapStrings(got))
	}

	for i := 0; i < 2; i++ {
		if _, _, ok := s.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	wantGaps(t, s.incomplete(f), "f.holders.1.notes", "f.holders.2.notes")

	// One note in the first holder answers that holder. The row is created
	// directly, as frame_test.go does: what is under test is the walk's
	// addressing, not which verb a client used to get the row.
	if _, _, ok := s.addAt(notes, instPath{{holders, 1}}); !ok {
		t.Fatal("Add refused the repeated field")
	}
	wantGaps(t, s.incomplete(f), "f.holders.2.notes")
}

func TestEveryGapAddressResolvesBackToWhatItNames(t *testing.T) {
	// The claim is DESIGN §6.3's remedy in one sentence: a report whose paths
	// cannot be sent back to `Update` is a screenshot rather than a result.
	// Each address is resolved against the same state it was produced from,
	// and must reach the declaration it was spelled from and a cell that can
	// hold the value it says is missing.
	img := mustLowerSource(t, checkSrc)
	s := newState(img)
	f := slotByPath(t, img, "f")
	write(t, &s, "f.gate", "y")
	holders := slotByPath(t, img, "f.holders")
	homes := slotByPath(t, img, "f.holders.homes")
	for i := 0; i < 2; i++ {
		if _, _, ok := s.addAt(holders, nil); !ok {
			t.Fatal("Add refused the collection")
		}
	}
	if _, _, ok := s.addAt(homes, instPath{{holders, 1}}); !ok {
		t.Fatal("Add refused the nested collection")
	}

	for _, g := range s.incomplete(f) {
		r, ok := s.resolve(g.path)
		if !ok {
			t.Errorf("%q resolves to nothing, so the gap cannot be fixed",
				g.path)
			continue
		}
		// Stripping the instance IDs out of the address must leave exactly
		// the declaration the walk was visiting. That is the round trip: the
		// same text, with the numbers the client had no part in choosing
		// removed, names the same slot.
		if decl := img.path(r.slot); withoutIDs(g.path) != decl {
			t.Errorf("%q resolves to %q, not to the declaration it was "+
				"spelled from", g.path, decl)
		}
		switch g.kind {
		case gapValue:
			// The cell exists and is empty. An address that reached no cell
			// at all would be unfixable by the edit this list invites.
			if !r.hasValue {
				t.Errorf("%q reaches no value, so it is not a missing value",
					g.path)
			}
			if r.cell.get().set {
				t.Errorf("%q holds a value and was still reported empty",
					g.path)
			}
		case gapRows:
			if img.slots[r.slot].coll == 0 {
				t.Errorf("%q was reported as needing instances and reaches "+
					"a slot that has none", g.path)
			}
		}
	}
}

// withoutIDs drops the segments of an address that are instance IDs, leaving
// the declaration path an address was built from.
func withoutIDs(path string) string {
	segs := strings.Split(path, ".")
	out := segs[:0]
	for _, s := range segs {
		if _, ok := parseInstanceID(s); ok {
			continue
		}
		out = append(out, s)
	}
	return strings.Join(out, ".")
}

func TestConditionalRequirednessIsAskedOnlyWhenTheGuardHolds(t *testing.T) {
	// The SYNTAX §7 example is the conformance baseline, and its
	// `shareholders required[.kind == COMPANY]` is the shape §5.2's second
	// line is about: no demand while the condition does not hold, one demand
	// per place the collection lives once it does. Inverting this test's
	// expectation is the commonest way to break it, so the two cases are
	// asserted back to back.
	img := lowerExample(t)
	s := newState(img)
	s.applyDefaults()
	application := slotByPath(t, img, "application")
	kind := slotByPath(t, img, "application.kind")
	shares := slotByPath(t, img, "application.shareholders")

	c, ok := s.cellOf(kind)
	if !ok {
		t.Fatal("application.kind has no cell")
	}

	// No kind entered at all: E6 leaves the guard unsatisfied, so the
	// collection is not demanded, and `internal` is pruned with its member.
	wantGaps(t, s.incomplete(application),
		"application.kind", "application.full_name")

	c.set(slotValue{set: true, i: int64(enumTag(t, img, kind, "PERSON"))})
	wantGaps(t, s.incomplete(application), "application.full_name")

	// A COMPANY asks for the collection and, once it has a row, for that
	// row's own required members at the row's address.
	c.set(slotValue{set: true, i: int64(enumTag(t, img, kind, "COMPANY"))})
	wantGaps(t, s.incomplete(application),
		"application.full_name", "application.shareholders")

	if _, _, ok := s.addAt(shares, nil); !ok {
		t.Fatal("Add refused the collection")
	}
	wantGaps(t, s.incomplete(application),
		"application.full_name",
		"application.shareholders.1.holder_name",
		"application.shareholders.1.shares")
}
