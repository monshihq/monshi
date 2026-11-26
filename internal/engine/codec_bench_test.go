package engine

// codec_bench_test.go prices the wire: what a durable store pays per case read,
// per draft save and per history append for the bytes codec.go writes. The rule
// that comparisons run as same-process pairs applies with full force here — a
// clone and an encode of the same snapshot are the two ways the same fact
// crosses the store boundary, and only a run where both saw the same allocator
// says which one costs more.

import (
	"runtime"
	"testing"

	"github.com/monshihq/monshi/internal/program"
)

// The eleven-commit fixture supplies a populated head, a real applicant draft
// and a history entry that mixes change kinds.
func benchWire(b *testing.B) (
	*program.Program, committedState, []byte, workingState, commitRecord,
) {
	h, trail, snaps := histTrail(b)
	cs := snaps[len(snaps)-1]
	rec := trail[len(trail)-1]
	w := stored(b, h.rt, h.dana).work[UserID("dana")]
	if w == nil {
		b.Fatal("the scripted case holds no draft for dana")
	}
	return cs.s.img, cs, encodeCommitted(cs), *w, rec
}

func BenchmarkWireSnapshot(b *testing.B) {
	img, cs, buf, _, _ := benchWire(b)
	// `clone` is the in-memory store's read of the same snapshot: what `Save`
	// pays to turn a committed frame into a mutable one, which is the work a
	// durable backend's `decode` replaces.
	b.Run("clone", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			st := cs.s.clone()
			runtime.KeepAlive(st)
		}
	})
	b.Run("encode", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out := encodeCommitted(cs)
			runtime.KeepAlive(out)
		}
	})
	b.Run("decode", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out, err := decodeCommitted(img, buf)
			if err != nil {
				b.Fatalf("decode: %v", err)
			}
			runtime.KeepAlive(out)
		}
	})
}

func BenchmarkWireDraft(b *testing.B) {
	img, _, _, ws, _ := benchWire(b)
	buf := encodeWorking(ws)
	b.Run("save", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out := encodeWorking(ws)
			runtime.KeepAlive(out)
		}
	})
	b.Run("read", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out, err := decodeWorking(img, buf)
			if err != nil {
				b.Fatalf("decode: %v", err)
			}
			runtime.KeepAlive(out)
		}
	})
}

func BenchmarkWireRecord(b *testing.B) {
	img, _, _, _, rec := benchWire(b)
	buf := encodeRecord(img, rec)
	b.Run("append", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out := encodeRecord(img, rec)
			runtime.KeepAlive(out)
		}
	})
	b.Run("read", func(b *testing.B) {
		b.ReportMetric(float64(len(buf)), "bytes/op")
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			out, err := decodeRecord(img, buf)
			if err != nil {
				b.Fatalf("decode: %v", err)
			}
			runtime.KeepAlive(out)
		}
	})
}
