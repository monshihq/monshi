package program

import "sort"

func (l *lowerer) dependencies() {
	n := len(l.img.slots)
	owners := make([][]NodeID, n)
	seen := make([]map[NodeID]bool, n)

	for c, reads := range l.reads {
		owner := l.condOwner[c]
		if owner == 0 {
			continue // an edge guard reads data but owns no path
		}
		for _, s := range reads {
			if s >= NodeID(n) {
				l.reportf("", "condition %d reads slot %d, not a declaration", c, s)
				continue
			}
			if seen[s] == nil {
				seen[s] = map[NodeID]bool{}
			}
			if seen[s][owner] {
				continue // one owner reads a slot through several operands
			}
			seen[s][owner] = true
			owners[s] = append(owners[s], owner)
		}
	}

	l.img.depFrom = make([]depRange, n)
	flat := make([]NodeID, 0, len(l.reads)*2)
	for s := 0; s < n; s++ {
		if len(owners[s]) == 0 {
			continue
		}
		sort.Slice(owners[s], func(i, j int) bool { return owners[s][i] < owners[s][j] })
		l.img.depFrom[s] = depRange{First: uint32(len(flat)), N: uint32(len(owners[s]))}
		flat = append(flat, owners[s]...)
	}
	l.img.depOwn = flat

	// §5.3's closure walk needs a queue bound known at lowering: no slot is
	// enqueued twice, so the number of slots is the bound.
	l.img.depBound = uint32(n)
}
