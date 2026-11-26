package engine

import "strings"

// report is the caller's list, with the count of entries that sit at content
// this actor may not view.
//
// A veiled one is named by its form and nothing more: the instance IDs inside a
// hidden collection are case data, and so is the fact that a particular field
// moved — which is the rule that a container grants nothing but its name,
// applied to the one other place this package turns an address into text.
//
// Duplicates are collapsed keeping the first, because two hidden clashes in one
// form degrade to the same string and a host reading "application, application"
// learns nothing new. Nothing else repeats: the merge settles each cell, and
// each collection at each place, exactly once — which is why this list can be
// longer than the gap list, whose walk visits one declaration per instance
// and needs the collapsing `gapPaths` does.
func (m *rebaser) report() ([]string, int) {
	if len(m.clashes) == 0 {
		return nil, 0
	}
	var (
		out    []string
		seen   = map[string]bool{}
		veiled int
	)
	for _, c := range m.clashes {
		var p string
		if c.veiled {
			// The count is of clashes, not of names: two hidden fields in one
			// form are two things the user cannot see, even though they can
			// only be pointed at once.
			veiled++
			// And the address is never rendered, not even as an alternative:
			// an instance ID inside a hidden collection is case data, and so
			// is the fact that this particular field moved.
			p = m.img.Path(m.img.Owner(c.slot))
		} else {
			p = m.out.addr(c.slot, c.path)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, veiled
}

// rebaseRefusal is the sentence a refused rebase carries beside its list. The
// count comes first, because a host that cannot show paths — a log line, an
// audit record — still says how much work the user has left; and the list is
// truncated, because `Conflicts` is where all of them are and a message that
// ran to three hundred paths would be a result set pasted into a sentence.
//
// The veiled count is stated because a form name is a puzzle otherwise: the
// actor is told to resolve a clash inside a subtree they cannot open, and the
// honest reading of that is `Discard`, which the sentence should not hide.
func rebaseRefusal(paths []string, veiled int) string {
	const shown = 4
	head, tail := paths, ""
	if len(head) > shown {
		tail = ", and " + dec(uint64(len(head)-shown)) + " more"
		head = head[:shown]
	}
	msg := count(len(paths), "path", "paths") +
		" the draft and the newer commits both changed: " +
		strings.Join(head, ", ") + tail +
		". The working copy is untouched; resolve them and rebase again"
	if veiled == 0 {
		return msg
	}
	return msg + " — " + count(veiled, "clash is", "clashes are") +
		" at content this actor may not view, and `Discard` is how those" +
		" are resolved"
}
