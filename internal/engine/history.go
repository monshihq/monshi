package engine

import (
	"context"
	"time"

	"github.com/monshihq/monshi/internal/program"
)

// HistoryResult is one page of one case's trail.
type HistoryResult struct {
	// Commits holds the page, oldest first. A host that wants the newest acts
	// first reverses it, and a page of one is its own answer either way.
	Commits []Commit

	// Next is the cursor to ask again with, or 0 when the trail is done. It is the
	// revision after the page's last entry, so a caller that walked to the end and
	// asks again gets an empty page rather than the whole history a second time —
	// and because the trail is dense, `Next` is also the first revision this caller
	// has not read, which is where a replay of what they missed would start.
	Next uint64

	// Revision is the case's head as this call read it, so a page can say how far
	// the case has moved since. It is the same number `View` reports and the same
	// one `Transition` and `Sign` want as `observed`.
	Revision uint64

	// Withheld counts the entries on this page the actor's mask did not reach. It
	// is a count and not a list: what it discloses is that the page is not the
	// whole story, which the reasoning about an unreadable rejection says a
	// caller is owed, and not which declaration moved or to what.
	Withheld int
}

// Commit is one entry in a case's history: the act, the hand, and what it wrote.
// It is the store's record as a host can address it, and the fields are the
// store's for the reasons store.go gives for them — a commit records new values
// and never old ones, and the stage is recorded per commit because a replay has no
// other place to learn where the case stood.
type Commit struct {
	// Revision is the number this commit produced, and `At` the moment the store
	// stamped it: the clock's word and not a caller's, which is what makes a trail
	// ordered by something a host cannot arrange.
	Revision uint64
	At       time.Time

	// By is the actor whose operation committed, and Op which kind of operation it
	// was. A verdict's author is the record's and not the entry's — the store keeps
	// one copy of a fact that could otherwise drift in two places — so reading a
	// verdict's account means reading these two fields beside it.
	By UserID
	Op CommitOp

	// Stage is where the case stood after this commit. A transition commit changes
	// nothing else, which is why the trail's stages and not a reconstructed
	// present are how a reader follows a case along its edges.
	Stage string

	// Changes are the entries the commit wrote, in the order the diff built
	// them: the case's declarations in preorder, a collection's rows after its own
	// set changed. Empty for a transition, and for a commit whose every entry sat
	// behind a branch this actor may not see.
	Changes []CommitChange
}

// CommitOp names which operation produced a commit. The four are the four that
// move a case's revision.
type CommitOp string

const (
	CommitSubmit     CommitOp = "SUBMIT"
	CommitTransition CommitOp = "TRANSITION"
	CommitReview     CommitOp = "REVIEW"
	CommitSign       CommitOp = "SIGN"
)

// name renders the store's word as the exported one. The switch is exhaustive by
// construction — the four constants above are the four `commitOp` ordinals — and an
// operation that joined them without a line here would answer with the empty
// string, which the tests refuse rather than leave to chance.
func (o commitOp) name() CommitOp {
	switch o {
	case opSubmit:
		return CommitSubmit
	case opTransition:
		return CommitTransition
	case opReview:
		return CommitReview
	case opSign:
		return CommitSign
	}
	return ""
}

// ChangeKind says what one entry of a commit is about, and which of
// `CommitChange`'s payloads it fills. The four are the semantic committed
// changes: a field's value, a collection's rows, a decision, an attestation.
//
// A host branches on the kind rather than on which payload is set, and that is the
// rule the store already follows internally: the effects, the merge and this
// read all ask the kind, because an unset payload means two different things
// depending on it — nothing at all for a value (a removal) and an empty collection
// for a rows entry.
type ChangeKind string

const (
	ChangeValue  ChangeKind = "VALUE"
	ChangeRows   ChangeKind = "ROWS"
	ChangeReview ChangeKind = "REVIEW"
	ChangeSign   ChangeKind = "SIGN"
)

// CommitChange is one entry of one commit: what moved, where, and what it holds
// now. Exactly one payload is filled, the one `Kind` names.
//
// `Path` is the text a host addresses by, and it names what the commit touched:
// for a value, the field at the instance it lives in, spelled
// "application.shareholders.42.who"; for a collection, the collection at the
// instance whose rows these are, "application.shareholders"; and for a verdict,
// the declaration it is about with no instance in it.
//
// The declaration half of that `Path` always resolves, and it is a consequence
// of the refusal this read makes before rendering anything: entries are keyed by
// slot ordinal (the numbering), and the drift check refuses a definition
// whose slot paths moved rather than reinterpret committed history into
// nonsense — so a path from an old commit cannot name a *different* field
// today. That refusal is the one place in the package where it does real work: a
// view addresses what the caller named, and a trail addresses what a past call
// recorded. The instance half may name a row that has since been removed, or a
// unit whose instance is gone, and that is the rule about dead addresses
// rather than an entry that failed to render.
type CommitChange struct {
	Kind ChangeKind
	Path string

	// Value is what the field holds after this commit, as the entry recorded
	// it. `Value.Kind() == KindNone` is the removal. It is rendered through the
	// definition the case is read under *now*, which is the reinterpretation
	// and the reason a semantic reload does not corrupt a trail rather than an
	// argument that the trail is frozen.
	Value Value

	// Rows lists the collection's stable row paths in creation order after this
	// change. An empty list means the collection was cleared.
	Rows []string

	// Review and Signature use the same shapes as View, but describe the
	// verdict at the time of this commit. Later edits can invalidate the
	// standing verdict without changing this entry.
	//
	// A revoked review has state PENDING and no standing reviewer. Commit.By
	// and Commit.Revision identify who revoked it and when.
	Review    ReviewInfo
	Signature SignatureInfo
}

// History reads the trail a case left.
//
// `cursor` is the first revision to read, and 0 means the beginning; `limit` is
// how many entries to answer with, and 0 means everything remaining. The page
// is oldest first and `Next` says where it stopped. A cursor past the head
// answers with an empty page rather than a refusal: the trail is dense, so
// "there is nothing at revision 900" and "there was nothing left to read" are
// the same fact, and a paging loop that stops on an empty page is the shape a
// host will write anyway. What the actor may not see is not on the page, and
// `Withheld` says that it is not. The record of an act survives its content,
// and a caller with no grant anywhere in the case still reads the case's trail
// of acts — which is not a disclosure past what `View` already makes: the head
// revision and the current stage are in every view result, and the trail's
// length *is* the head revision, so filtering envelopes would hide nothing
// while destroying the audit.
//
// The refusals are three, and none of them is about history being empty:
//
// - a negative `limit` is INVALID_OPERATION, because a caller that meant
// "everything" can say 0 and one that meant "the newest ten" is about to get an
// answer it did not ask for; - a definition whose slot paths moved since this
// case's data was written is SCHEMA_MISMATCH, before a single entry is rendered
// (the drift check, the same one `View` and `Update` pay), because an entry's
// address is an ordinal and reading it through a relabelled definition would
// launder the relabel as history; - an unreadable case is NOT_FOUND, as every
// operation says it.
func (c Case) History(ctx context.Context, cursor uint64,
	limit int) (HistoryResult, error) {
	refuse := c.refusing("history")
	d, rec, err := c.look(ctx, refuse)
	if err != nil {
		return HistoryResult{}, err
	}
	if limit < 0 {
		return HistoryResult{}, refuse.now(InvalidOp,
			"a page cannot hold a negative number of commits")
	}
	img := d.img
	res := HistoryResult{Revision: rec.revision}

	// One fold of the mask for the whole page, at the roles the committed state
	// carries and the stage the case sits at — the read and not a new one, which
	// is what makes the two agree. The cost is bounded by the definition and not by
	// the trail, so a case with a long history and a short page pays for the slots,
	// the entries it answers with, and nothing in between.
	roles := rolesHeld(img, c.actor, rec.commit.s)
	stage := rec.commit.stage
	vis := viewable(img, roles, stage)

	page, err := c.historyThrough(ctx, refuse, cursor, rec.revision, limit)
	if err != nil {
		return HistoryResult{}, err
	}
	for _, com := range page {
		commit, hid := commitView(img, com, roles, stage, vis)
		res.Withheld += hid
		res.Commits = append(res.Commits, commit)
	}
	if last := len(page) - 1; last >= 0 && page[last].revision < rec.revision {
		res.Next = page[last].revision + 1
	}
	return res, nil
}

// commitView renders one stored entry as the exported `Commit` under a
// reader's mask, and counts the entries it withheld. `History` pages it per
// trail, and hooks.go's `notify` runs it once per commit: the same function for
// both, so a notification and a later read of the same entry cannot drift
// about what one commit said — the pair is the whole argument for making
// the renderer an export-shaped function rather than a loop body.
func commitView(img *program.Program, com commitRecord, roles []program.RoleID,
	stage program.StageID, vis []bool) (Commit, int) {
	out := Commit{
		Revision: com.revision,
		At:       com.at,
		By:       com.by,
		Op:       com.op.name(),
		Stage:    img.StageName(com.stage),
	}
	withheld := 0
	for _, ch := range com.changes {
		if !disclosed(img, roles, stage, vis, ch) {
			withheld++
			continue
		}
		out.Changes = append(out.Changes, entryOf(img, com, ch))
	}
	return out, withheld
}

// disclosed answers the one question this read cannot borrow an answer to from a
// view's shape: whether this actor's grant reaches what one stored entry carries.
//
// For a row set it is `viewable`'s answer, because a container's instances are how
// a granted child is addressable at all and the view discloses the same names to
// the same actors. For everything else it is the grant at the node itself: a
// field's value, a decision *about* a unit, an attestation on a field.
//
// The difference matters exactly once, and it is worth the separate line. A
// subtree grant marks every ancestor reachable — a container nobody may view
// carrying content somebody may cannot explain where its own child came from — so
// `viewable` would let an actor who may read a form's rows read the registrar's
// decision about the form, which is content of the unit and not a path to a row. A
// view is not exposed to that: it renders the verdict at the node, and the node it
// renders is the one the grant names. Asking `effective` is the same permission
// source `viewable` itself consults, so a trail and a form cannot drift about who
// may see what, and the cost is one lookup per entry on the page rather than a
// second fold over the slots.
func disclosed(img *program.Program, roles []program.RoleID, stage program.StageID, vis []bool,
	ch commitChange) bool {
	if ch.kind == chgRows {
		return vis[ch.slot]
	}
	return img.Effective(roles, stage, ch.slot)&program.PermView != 0
}

// entryOf renders one stored entry as the shape a host addresses. The payloads
// come from the seams the rest of the package already uses: the same path spine
// `View` and the gap reports write, the same value loader, and the two verdict
// derivations replay uses — so a trail and a state answer a question about one
// act the same way.
func entryOf(img *program.Program, com commitRecord, ch commitChange) CommitChange {
	out := CommitChange{Path: addr(img, ch.slot, ch.path)}
	switch ch.kind {
	case chgValue:
		out.Kind = ChangeValue
		out.Value = load(img, ch.slot, ch.v)
	case chgRows:
		out.Kind = ChangeRows
		out.Rows = rowPaths(img, ch.slot, ch.path, ch.ids)
	case chgReview:
		out.Kind = ChangeReview
		st := derivedDecision(com, ch)
		out.Review = ReviewInfo{Unit: out.Path, State: st.state(),
			By: st.by, At: st.at, Comment: st.note}
	case chgSign:
		out.Kind = ChangeSign
		g := derivedSignature(com, ch)
		out.Signature = SignatureInfo{By: g.by, At: g.at, Evidence: g.ref}
	}
	return out
}

// rowPaths spells a committed row set as the addresses that name it: the
// collection's own path at the instance it lives in, then one more step per
// instance in creation order. The IDs come from the entry and not from the
// case. The renderer consults no data (image.go), which is what lets a trail
// name a dead instance at all.
func rowPaths(img *program.Program, coll program.NodeID, host instPath,
	ids []instanceID) []string {
	if len(ids) == 0 {
		return nil
	}
	root := addr(img, coll, host)
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = root + "." + dec(uint64(id))
	}
	return out
}
