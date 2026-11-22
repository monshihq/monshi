package monshi

// RUNTIME §8's harness. What is measured here is what is shipped:
// lowering, the two hot reads the image exists to make cheap — one
// condition closure, one permission resolve — path resolution, and one
// accepted edit batch. The view, fanout and transition benchmarks need
// operations that do not exist yet, so they are not faked here; the tree
// baseline §8 compares against needs the same operations too, and
// arrives with them.
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
func parse(b *testing.B, src string) *schema.Schema {
	b.Helper()
	s, err := schema.Parse(strings.NewReader(src))
	if err != nil {
		b.Fatalf("schema.Parse: %v", err)
	}
	if len(s.Errors) != 0 {
		b.Fatalf("definition does not compile: %v", s.Errors)
	}
	return s
}

// mustLower is compile() without a *testing.T to insist on.
func mustLower(b *testing.B, id, src string) *image {
	b.Helper()
	img, err := lower(id, parse(b, src))
	if err != nil {
		b.Fatalf("lower: %v", err)
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
// stages, and one collection whose instances a request would carry.
func wideSrc(b *testing.B) string {
	b.Helper()
	var w strings.Builder
	w.WriteString("roles {\n")
	for r := 0; r < 6; r++ {
		name := "R" + strconv.Itoa(r)
		w.WriteString("    " + name + " { allow view all; }\n")
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

// --- lookups by name, for fixtures only (RUNTIME §3.9's seam is not
// shipped yet, and these read the closed arrays directly) ---

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
