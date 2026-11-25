package engine

import (
	"context"
	"math"
)

// historyPage converts one-based revision numbers to safe slice indexes.
func historyPage[T any](entries []T, cursor uint64, limit int) []T {
	cursor = max(cursor, 1)
	if cursor > uint64(len(entries)) {
		return nil
	}
	page := entries[int(cursor-1):]
	if limit > 0 && limit < len(page) {
		page = page[:limit]
	}
	return page
}

// historyThrough pins the last readable revision to the head whose data and
// permissions the caller is using. History is append-only and dense.
func (c Case) historyThrough(ctx context.Context, r refuser, cursor, head uint64, limit int) ([]commitRecord, error) {
	from := max(cursor, 1)
	if from > head {
		return nil, nil
	}
	n := int(min(head-from+1, uint64(math.MaxInt)))
	if limit > 0 {
		n = min(n, limit)
	}
	page, err := c.w.cases.history(ctx, r, from, n)
	if err != nil {
		return nil, err
	}
	if len(page) != n {
		return nil, r.now(StorageFailure, "history does not cover the requested revision window")
	}
	for i := range page {
		if page[i].revision != from+uint64(i) {
			return nil, r.now(StorageFailure, "history revisions are not consecutive")
		}
	}
	return page, nil
}
