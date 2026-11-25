package program

import (
	"testing"

	"github.com/monshihq/monshi/internal/testfixture"
	"github.com/monshihq/monshi/schema"
)

// mustLower is compile() without a *testing.T to insist on, and a TB so a
// test can build the same fixture a benchmark times.
func mustLower(tb testing.TB, id, src string) *Program {
	tb.Helper()
	img, err := Compile(id, testfixture.Parse(tb, src))
	if err != nil {
		tb.Fatalf("lower: %v", err)
	}
	return img
}

// lowerOnly runs just lowering on a pre-compiled definition.
func lowerOnly(b *testing.B, id string, s *schema.Schema) *Program {
	b.Helper()
	img, err := Compile(id, s)
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
		if img.SlotCount() != 20 {
			b.Fatalf("image has %d slots, want the §7 example's 20",
				img.SlotCount())
		}
	}
}

// BenchmarkLoweringOnly separates §9's serialization argument from the
// front end it would save: this is the rebuild cost alone.
func BenchmarkLoweringOnly(b *testing.B) {
	s := testfixture.Parse(b, specExample(b))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		img := lowerOnly(b, "bench", s)
		if img.ConditionCount() < 2 {
			b.Fatalf("image has %d conditions", img.ConditionCount())
		}
	}
}

func BenchmarkLoweringWide(b *testing.B) {
	src := testfixture.Wide(b)
	b.ReportAllocs()
	var slots int
	for i := 0; i < b.N; i++ {
		img := mustLower(b, "wide", src)
		slots = img.SlotCount()
	}
	b.ReportMetric(float64(slots), "slots/op")
}

// BenchmarkLoweringWideOnly is §9's serialization argument on the corpus
// it describes: the 51-slot image rebuilds in microseconds, so shipping
// images instead of source buys nothing measurable.
func BenchmarkLoweringWideOnly(b *testing.B) {
	s := testfixture.Parse(b, testfixture.Wide(b))
	b.ReportAllocs()
	var slots int
	for i := 0; i < b.N; i++ {
		slots = lowerOnly(b, "wide", s).SlotCount()
	}
	b.ReportMetric(float64(slots), "slots/op")
}
