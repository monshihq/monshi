package engine

// The cost of the exported contract (DESIGN §8, RUNTIME §8). Milestone
// 4F2's claim is that a host's `Storage` costs the *encoding*, not a
// redesign of the engine's work — and RUNTIME §8's rule about claims of
// that shape is that they are measured as same-process pairs. The pair
// here is `BenchmarkSubmit` (the default seam, structs all the way) and
// `BenchmarkSubmitViaContract` (the same operations over the exported
// interface, bytes on the way in and out) run in one `go test -bench`
// invocation; the ratio between the two rows is the contract's price,
// and no other comparison is offered because two processes are not one
// machine's moment.

import "testing"

// benchContractCase is `benchSpecCase` through the contract: the same §7
// corpus, one case, built the way a host of `NewRuntimeWithStorage`
// builds it. The registry is deliberately still the runtime's own —
// definitions were never the store's to hold (DESIGN §3).
func benchContractCase(b *testing.B) Case {
	b.Helper()
	rt := NewRuntimeWithStorage(NewMemStorage())
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

// BenchmarkSubmitViaContract is the pair to `BenchmarkSubmit`: the same
// three-write batch and the same submit, run over the exported storage
// contract with the library's own `MemStorage` behind it. What this row
// charges on top of the default one is the codec's arithmetic — the
// snapshot encoded at commit, the working copies encoded at save and
// decoded at read, the entry encoded for the trail — and the sentinels
// travelling out as *Storage* method calls instead of struct-field
// reads. It is an in-process upper bound only in one direction: a real
// host's backend adds its own I/O, which is its own row to measure; the
// number here is what the contract itself costs, so a host can tell the
// interface's price from its storage engine's.
func BenchmarkSubmitViaContract(b *testing.B) {
	c := benchContractCase(b)
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
