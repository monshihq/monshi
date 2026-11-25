package engine

import (
	"encoding/hex"
	"testing"
	"time"
)

// These bytes came from the original root-package codec, not this encoder.
// The first submit in histTrail exercises enum, text, defaults, and row IDs.
func TestDecodePreRefactorStorage(t *testing.T) {
	h, trail, snaps := histTrail(t)
	decode := func(s string) []byte {
		t.Helper()
		b, err := hex.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	head, err := decodeCommitted(h.img, decode("010204020801030103416461040301310501076372656174656402070201000002000009000100000000"))
	if err != nil || !sameCommitted(h.img, head, snaps[1]) {
		t.Fatalf("stored head no longer matches revision 1: %v", err)
	}
	draft, err := decodeWorking(h.img, decode("010103020402080103010341646104030131050107637265617465640207020100000200000900"))
	if err != nil || draft.base != 1 || draft.rev != 3 || !sameState(h.img, draft.s, snaps[1].s) {
		t.Fatalf("stored working copy no longer matches its base, revision, and content: %v", err)
	}
	entry, err := decodeRecord(h.img, decode("0101f6010000035554430464616e610101030102000108010001030001010341646100020600010c0400020102"))
	want := trail[0]
	want.at = time.Unix(123, 0).UTC()
	if err != nil || !sameRecord(h.img, entry, want) {
		t.Fatalf("stored entry no longer matches the first submit: %v", err)
	}
}
