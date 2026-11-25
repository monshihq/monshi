package program

// viewChanges is §5.3's affected set for the one operation that writes no
// value: a stage move, which changes which paths an actor may see by changing
// the stage the masks are resolved at (§3.5's overlays) and nothing else.
//
// The walk is a definition walk rather than a data walk, and that is the whole
// cost story. A condition never reads the stage — `Predicate` is a closure over
// one row (RUNTIME §3.4) — so moving a case between stages cannot change what
// is present, what is required, or what any value is: only the permission fold
// takes the stage as an input. So this compares one word per slot and reports
// the topmost difference, and no cell is touched. A transition's affected list
// answers "which views of this case may now be rendered differently", which is
// a question about the actor and the definition rather than about the data.
//
// The collapse rule is §5.3's own: preorder makes an ancestor's range
// contiguous, so the first differing slot is reported and everything it covers
// is skipped, which leaves the result sorted in declaration order with no path
// contained in another. Reporting the group rather than the sixteen fields
// inside it is the honest granularity, because a client told "this subtree may
// render differently" re-reads the subtree.
func (img *Program) ViewChanges(roles []RoleID, from, to StageID) []string {
	if from == to || !img.overlaysDiffer(roles, from, to) {
		return nil
	}
	var out []string
	cover := NodeID(0) // end of the last reported subtree
	for n := NodeID(1); n < NodeID(len(img.slots)); n++ {
		if n < cover {
			continue // inside a subtree already reported by its own root
		}
		was := img.Effective(roles, from, n)&PermView != 0
		now := img.Effective(roles, to, n)&PermView != 0
		if was == now {
			continue
		}
		out = append(out, img.Path(n))
		_, cover = img.Subtree(n)
	}
	return out
}

// overlaysDiffer answers whether any role the actor holds resolves a
// *different* overlay at `to` than it did at `from`. If none does, then
// `permAt` hands back the same allow and deny word for every slot at both
// stages, because a stage reaches §3.5's algebra only by selecting an overlay —
// so the walk above would compare len(slots) identical pairs and report
// nothing. Definitions with no stage-scoped grants are the common case, and the
// test costs one binary search per held role.
//
// Which is a claim about cost and not about the answer: replacing the test with
// "always differ" changes no result a test can see, and that is the intended
// reading — the shortcut may only skip work, never verdicts. What is checked is
// the other direction, that a role whose overlay index *does* move is not
// skipped, because a shortcut that swallows one of those is a lost refresh.
func (img *Program) overlaysDiffer(roles []RoleID, from, to StageID) bool {
	for _, r := range roles {
		if r == 0 {
			continue // ordinal 0 is "no role": it sees the same at any stage
		}
		over := img.perms.byRole[r].over
		if overlayIndex(over, from) != overlayIndex(over, to) {
			return true
		}
	}
	return false
}

// roleChanges is `viewChanges`' mirror, asked of the argument a commit can
// move and a stage move cannot: two role sets at the one stage the case sits
// at. The skip that keeps the common case free is the caller's own — equal
// folded lists mean every slot was handed the same inputs, which is
// overlaysDiffer's argument transposed to the other parameter, and it is a
// claim about cost in the same way: the walk may only skip work, never a
// verdict.
func (img *Program) RoleChanges(from, to []RoleID,
	stage StageID) []NodeID {
	var out []NodeID
	cover := NodeID(0)
	for n := NodeID(1); n < NodeID(len(img.slots)); n++ {
		if n < cover {
			continue
		}
		was := img.Effective(from, stage, n)&PermView != 0
		now := img.Effective(to, stage, n)&PermView != 0
		if was == now {
			continue
		}
		out = append(out, n)
		_, cover = img.Subtree(n)
	}
	return out
}

// mayEdit is the same question for roles already folded, which is what an
// operation that asks it of several slots uses: the fold walks the
// definition's role-typed fields, and §3.5's masks are bitwise algebra over
// the result, so folding once per request and asking per slot is the shape
// that keeps §8's permission-resolve cost honest. Splitting the two is also
// what makes the *source* of the roles impossible to get wrong at the call
// site — `Update` folds from the committed snapshot and cannot accidentally
// ask about a slot against the candidate it is writing.
func (img *Program) MayEdit(roles []RoleID, stage StageID, n NodeID) bool {
	return img.Effective(roles, stage, n)&PermEdit != 0
}

// maySubmit is the question a `Submit` asks, and it is asked of the *form*
// rather than of anything inside it. SYNTAX P3 is why: a `submit` grant's
// target must be a top-level form, so there is no field-level submit to
// reconcile and no member's mask to fold. The consequence is worth stating
// because it reads as a hole: an actor with `submit` on a form can publish
// every value in it, including fields they may not `edit` — and that is the
// definition's own decision, since a draft can only have been written by
// somebody with `edit` or come from a default the author declared.
//
// The masks are consulted at the *committed* stage, which is the only stage
// a submit can act at: §4.4 keeps the stage where a transition put it, and
// §5.1 makes the working copy's stage a reference rather than a field. A
// draft that would satisfy some other stage's grant does not submit under it.
func (img *Program) MaySubmit(roles []RoleID, stage StageID, form NodeID) bool {
	return img.Effective(roles, stage, form)&permSubmit != 0
}

// mayReview is the question `Review` asks, and it is asked at the slot the
// caller's path resolves to. SYNTAX P3 requires a `review` grant's target to
// be a `verifiable` declaration and P2 guarantees the runtime never resolves a
// target, so the bit at a member's slot can only have arrived there by
// inheritance — meaning asking a *unit's* own slot is the same answer with one
// fewer questions to get wrong, and asking at the resolved slot is what lets an
// actor who cannot decide there learn nothing about what governs it.
//
// A `verifiable` *group* therefore has no grant of its own unless the
// definition writes one: its slot inherits its parent's, which is §3.5's
// inheritance and not an exception. A host that wants only a specific reviewer
// for one group says so at that group's path.
func (img *Program) MayReview(roles []RoleID, stage StageID, n NodeID) bool {
	return img.Effective(roles, stage, n)&permReview != 0
}

// maySign is the question `Sign` asks, and it is asked at the signature field's
// own slot for the reason `mayReview` gives about its unit: P3 puts a `sign`
// grant's target on a signature field, so the bit can arrive at any other slot
// only by inheritance from an ancestor — and `allow sign all;` is exactly that,
// naming the case root and reaching every field beneath it. That is the reading
// P2 gives `all` in every other permission's hands, and it means the mask cannot
// be the whole authority question: sign.go asks `sigEntry`'s second conjunct
// beside it, because a bit inherited from the root says the definition opened
// this act to a role and S3 says which hand it opened it to.
func (img *Program) MaySign(roles []RoleID, stage StageID, n NodeID) bool {
	return img.Effective(roles, stage, n)&permSign != 0
}

// defaultOf is the value one slot holds when a case or an instance is created
// and nothing writes to it — the same answer `materialize` and `defaults`
// write, reached without a state to write it into. §5.8's merge needs it
// because the version a rebase compares against is not always in history: a
// cell no commit ever wrote holds its default, and treating that as absence
// would make an untouched defaulted field look like the draft's own edit.
func (img *Program) DefaultOf(n NodeID) Cell {
	if def := img.slots[n].Def; def != 0 {
		return img.ConstOf(def)
	}
	return Cell{}
}

// bindingName renders what a definition declared a hook against — a custom
// type's name or a field's path — which is the only name a host's log line has
// for the function. It reads the image's own table rather than the caller's
// text, so the refusal about a hook says what the definition said.
func (img *Program) BindingName(id HookID) string {
	if id == 0 || int(id) >= len(img.hooks) {
		return ""
	}
	return img.Name(img.hooks[id].name)
}

// bindingText resolves the image's key for a hookID to the (kind, name) pair
// a host registers against, or false for no binding at all. One place reads
// `img.hooks`, so `normalizer` and `provider` cannot disagree about what a
// binding id means the way two hand-written lookups could.
func (img *Program) BindingText(id HookID) (uint8, string, bool) {
	if id == 0 || int(id) >= len(img.hooks) {
		return 0, "", false
	}
	key := img.hooks[id]
	return key.kind, img.Name(key.name), true
}

// child finds a direct child by name, in declaration order. The scan is bounded
// by declaration breadth, which is a fact about the definition known before any
// request arrives, and member names are unique within a container (N3), so the
// first match is the only one. Names are lowercase-initial and IDs are digits
// (SYNTAX §3.3-N2), so the two can never be confused for each other.
func (img *Program) Child(cur NodeID, name string) (NodeID, bool) {
	for _, c := range img.ChildrenOf(cur) {
		if img.Name(img.slots[c].Name) == name {
			return c, true
		}
	}
	return 0, false
}

func (img *Program) FormByName(name string) (FormID, bool) {
	for i, f := range img.forms {
		if i == 0 {
			continue // ordinal 0 is "no form"
		}
		if img.Name(img.slots[f].Name) == name {
			return FormID(i), true
		}
	}
	return 0, false
}

func (img *Program) StageByName(name string) (StageID, bool) {
	for i := range img.stages {
		if i == 0 {
			continue // ordinal 0 is "no stage"
		}
		if img.Name(img.stages[i].Name) == name {
			return StageID(i), true
		}
	}
	return 0, false
}

func (img *Program) RoleByName(name string) (RoleID, bool) {
	for i, n := range img.roleIdx {
		if i == 0 {
			continue // ordinal 0 confers nothing
		}
		if img.Name(n) == name {
			return RoleID(i), true
		}
	}
	return 0, false
}

func (img *Program) TagByName(name string) (TagID, bool) {
	for i, n := range img.tagNames {
		if i == 0 {
			continue
		}
		if img.Name(n) == name {
			return TagID(i), true
		}
	}
	return 0, false
}

// same is the equality a commit diff and a no-op submit are decided on: does
// this slot hold the same value in these two payloads.
//
// Each class is compared by what it *means* rather than by its bytes, and
// two of those choices are worth stating. A decimal compares by value, so
// `1.0` over a stored `1.00` is not a content change — shopspring keeps the
// written scale in the representation, and §5.1's diff records what the case
// now says, not how a client typed it. A time compares by instant, so a
// value that arrived through a different `*Location` is the same datetime;
// `Date` and `DateTime` both drop to the wall clock here rather than to the
// struct's pointer fields, which is the difference between a diff that means
// something and one that reports a reformat.
//
// An unset payload equals another unset payload: `Empty` over a field that
// was already empty is no change, which is §6.3's "no meaningless commit"
// read at the level of one cell.
func (img *Program) Same(n NodeID, a, b Cell) bool {
	if a.Set != b.Set {
		return false
	}
	if !a.Set {
		return true
	}
	if img.slots[n].Role != 0 {
		return a.Text == b.Text
	}
	switch img.BaseOf(n) {
	case BaseString, BaseOptions:
		return a.Text == b.Text
	case BaseInteger, BaseBoolean, BaseEnum:
		return a.Int == b.Int
	case BaseDecimal:
		return a.Decimal.Equal(b.Decimal)
	case BaseDate, BaseDatetime:
		return a.Time.Equal(b.Time)
	case BaseBinary:
		return string(a.Binary) == string(b.Binary)
	}
	return false
}
