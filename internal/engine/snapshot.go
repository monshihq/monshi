package engine

import (
	"time"

	"github.com/monshihq/monshi/internal/program"
)

// initialState is the case a `Create` stores: defaults materialized into
// an empty frame at the definition's initial stage (DESIGN §5, RUNTIME
// §5.1), with no history. Both store paths build it here, because "what an
// empty case holds" is one rule and not a per-backend convention.
func initialState(img *program.Program) committedState {
	st := newState(img)
	st.applyDefaults()
	return committedState{s: st, stage: img.InitialStage()}
}

// commitOp identifies the operation in a persisted history entry.
type commitOp uint8

const (
	opSubmit commitOp = iota + 1
	opTransition
	opReview
	opSign
)

// commitChange is a forward diff. A row-set change carries stable IDs in
// creation order; review and signature entries are verdicts, not content.
type commitChange struct {
	kind uint8
	slot program.NodeID
	path instPath
	v    program.Cell
	ids  []instanceID // chgRows: the committed set, in creation order
}

const (
	chgValue  uint8 = iota + 1 // one field's value, at one instance path
	chgRows                    // one collection's instance count
	chgReview                  // one verifiable unit's decision: not content
	chgSign                    // one signature field's signer: not content
)

// commitRecord is an immutable forward diff at one revision, including its author, time and resulting stage.
type commitRecord struct {
	revision uint64
	at       time.Time
	by       UserID
	op       commitOp
	stage    program.StageID
	changes  []commitChange
}

// committedState holds committed content and its verdicts. Drafts clone only
// the content. Verdict tables are copy-on-write to keep published snapshots
// immutable.
type committedState struct {
	s     state
	stage program.StageID

	reviews []reviewState
	sigs    []sigState
}

// stageName renders the snapshot's stage as its definition spells it.
func (cs committedState) stageName() string { return cs.s.img.StageName(cs.stage) }

// workingState is one actor's uncommitted copy: the committed revision it
// diverged from, its own revision as the write guard, and the candidate
// data. DESIGN §6.5's rebase compares `base` against the case's committed
// revision; §6.2's refusal compares `rev` against the one an edit presented.
type workingState struct {
	base uint64
	rev  uint64
	s    state
}

// clone hands out a private copy of a working copy. Two of the store's
// guarantees rest on it: an abandoned candidate cannot leak into the next
// session, and a saved candidate cannot be edited behind the store's back.
// The cost is one row copy per operation, which is the cost §5.1's frame
// already pays — a working copy *is* a frame's candidate half, so this is
// the same bytes moved once, not twice.
func (w *workingState) clone() workingState {
	return workingState{base: w.base, rev: w.rev, s: w.s.clone()}
}

// caseHead is all a lookup hands to an operation. Drafts and history have
// their own reads; the backend never shares a mutable map with its caller.
type caseHead struct {
	def      string
	revision uint64
	commit   committedState
}
