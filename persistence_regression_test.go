package monshi_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/monshihq/monshi"
)

func TestRestartChecksLayoutBeforeReadingValues(t *testing.T) {
	s := monshi.NewMemStorage()
	r := monshi.NewRuntimeWithStorage(s)
	define(t, r, "test", trailSrc)
	c, err := r.Create(ctx(), "test", trailOwner)
	if err != nil {
		t.Fatal(err)
	}
	typed(t, c)
	for _, replacement := range [][2]string{{"PERSON, COMPANY", "COMPANY, PERSON"}, {"full_name", "given_name"}} {
		r := monshi.NewRuntimeWithStorage(s)
		define(t, r, "test", strings.ReplaceAll(trailSrc, replacement[0], replacement[1]))
		_, err := r.Open(ctx(), c.ID(), trailOwner)
		var e *monshi.CaseError
		if !errors.As(err, &e) || e.Reason != monshi.SchemaMismatch {
			t.Fatalf("replacing %s: %v", replacement[0], err)
		}
	}
}

func TestTypedReviewHistory(t *testing.T) {
	src := strings.NewReplacer("application verifiable {", "application {", "kind      : legal_form required;", "kind      : legal_form required verifiable;", "allow review application;", "allow review application.kind;").Replace(trailSrc)
	for _, durable := range []bool{false, true} {
		var s monshi.Storage
		if durable {
			s = monshi.NewMemStorage()
		}
		rt := monshi.NewRuntimeWithStorage(s)
		define(t, rt, "typed", src)
		c, err := rt.Create(ctx(), "typed", trailOwner)
		if err != nil {
			t.Fatal(err)
		}
		rev := typed(t, c)
		reviewer, err := rt.Open(ctx(), c.ID(), trailRuler)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reviewer.Review(ctx(), "application.kind", monshi.ReviewReject, rev, "check registration"); err != nil {
			t.Fatal(err)
		}
		if durable {
			rt = monshi.NewRuntimeWithStorage(s)
			define(t, rt, "typed", src)
			reviewer, err = rt.Open(ctx(), c.ID(), trailRuler)
			if err != nil {
				t.Fatal(err)
			}
		}
		h, err := reviewer.History(ctx(), rev+1, math.MaxInt)
		if err != nil || len(h.Commits) != 1 || len(h.Commits[0].Changes) != 1 {
			t.Fatalf("history: %+v, %v", h, err)
		}
		got := h.Commits[0].Changes[0].Review
		if got.State != monshi.ReviewRejected || got.Comment != "check registration" {
			t.Fatalf("durable=%t: %+v", durable, got)
		}
		page, err := reviewer.History(ctx(), math.MaxUint64, math.MaxInt)
		if err != nil || len(page.Commits) != 0 {
			t.Fatalf("extreme cursor: %+v, %v", page, err)
		}
	}
}

// Run a second writer after the operation has read its head but before the
// backend takes its history slice. No goroutine scheduling is involved.
type betweenReads struct {
	monshi.Storage
	beforeHistory func()
}

type betweenInitializers struct {
	monshi.Storage
	beforeInsert func()
}

func (s *betweenInitializers) InitDraft(c context.Context, id monshi.CaseID, u monshi.UserID, w monshi.WorkingRecord) (monshi.WorkingRecord, error) {
	if f := s.beforeInsert; f != nil {
		s.beforeInsert = nil
		f()
	}
	return s.Storage.InitDraft(c, id, u, w)
}

func TestFirstDraftCannotReplaceAnotherSessionsEdit(t *testing.T) {
	s := &betweenInitializers{Storage: monshi.NewMemStorage()}
	rt := monshi.NewRuntimeWithStorage(s)
	define(t, rt, "test", trailSrc)
	c, err := rt.Create(ctx(), "test", trailOwner)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Discard(ctx(), 0); err != nil {
		t.Fatal(err)
	}
	s.beforeInsert = func() {
		if _, err := c.Update(ctx(), 0, monshi.Set("application.full_name", monshi.Text("first"))); err != nil {
			t.Fatal(err)
		}
	}
	_, err = c.Update(ctx(), 0, monshi.Set("application.full_name", monshi.Text("second")))
	var e *monshi.CaseError
	if !errors.As(err, &e) || e.Reason != monshi.StaleRevision {
		t.Fatalf("delayed edit: %v", err)
	}
	w, ok, err := s.Draft(ctx(), c.ID(), trailOwner.User)
	if err != nil || !ok || w.Revision != 1 {
		t.Fatalf("winning draft: %+v, %v", w, err)
	}
}

func (s *betweenReads) History(c context.Context, id monshi.CaseID, from uint64, limit int) ([]monshi.HistoryEntry, error) {
	if f := s.beforeHistory; f != nil {
		s.beforeHistory = nil
		f()
	}
	return s.Storage.History(c, id, from, limit)
}

func TestReadsStopAtObservedHead(t *testing.T) {
	s := &betweenReads{Storage: monshi.NewMemStorage()}
	rt := monshi.NewRuntimeWithStorage(s)
	define(t, rt, "test", trailSrc)
	c, err := rt.Create(ctx(), "test", trailOwner)
	if err != nil {
		t.Fatal(err)
	}
	typed(t, c)
	actor := trailOwner
	actor.User = "other"
	other, err := rt.Open(ctx(), c.ID(), actor)
	if err != nil {
		t.Fatal(err)
	}
	draft(t, other, monshi.Set("application.full_name", monshi.Text("Grace")))
	write := func(name string) {
		draft(t, c, monshi.Set("application.full_name", monshi.Text(name)))
		committed(t, c, "application")
	}
	s.beforeHistory = func() {
		draft(t, c, monshi.Set("application.kind", monshi.Tag("COMPANY")))
		committed(t, c, "application")
	}
	page, err := other.History(ctx(), 1, 0)
	if err != nil || page.Revision != 1 || len(page.Commits) != 1 {
		t.Fatalf("history: %+v, %v", page, err)
	}
	s.beforeHistory = func() { write("Alan") }
	res, err := other.Rebase(ctx())
	if err != nil || res.Base != 2 {
		t.Fatalf("rebase: %+v, %v", res, err)
	}
	res, err = other.Rebase(ctx())
	if err == nil || len(res.Conflicts) != 1 || res.Conflicts[0] != "application.full_name" {
		t.Fatalf("next rebase: %+v, %v", res, err)
	}
}

func TestOpaqueBufferOwnership(t *testing.T) {
	buf := []byte{1}
	v := monshi.Binary(buf)
	buf[0] = 2
	v.Binary()[0] = 3
	if v.Binary()[0] != 1 {
		t.Fatal("binary value aliases caller bytes")
	}
	s := monshi.NewMemStorage()
	id, err := s.Create(ctx(), monshi.CaseRecord{State: buf})
	if err != nil {
		t.Fatal(err)
	}
	buf[0] = 4
	head, err := s.Load(ctx(), id)
	if err != nil || head.State[0] != 2 {
		t.Fatalf("created head: %+v, %v", head, err)
	}
	w, err := s.InitDraft(ctx(), id, "a", monshi.WorkingRecord{State: buf})
	if err != nil {
		t.Fatal(err)
	}
	w.State[0] = 5
	buf[0] = 5
	w, err = s.InitDraft(ctx(), id, "a", monshi.WorkingRecord{State: []byte{9}})
	if err != nil || w.State[0] != 4 || w.Revision != 0 {
		t.Fatalf("existing draft: %+v, %v", w, err)
	}
	if err := s.Save(ctx(), id, "a", 0, monshi.WorkingRecord{State: buf}); err != nil {
		t.Fatal(err)
	}
	tx := monshi.Transaction{Head: monshi.CaseRecord{Revision: 1, State: buf}, Entry: buf}
	if _, err := s.Commit(ctx(), id, tx); err != nil {
		t.Fatal(err)
	}
	buf[0] = 6
	for range 2 {
		h, err := s.Load(ctx(), id)
		if err != nil || h.State[0] != 5 {
			t.Fatalf("head: %+v, %v", h, err)
		}
		h.State[0] = 7
		w, ok, err := s.Draft(ctx(), id, "a")
		if err != nil || !ok || w.State[0] != 5 {
			t.Fatalf("draft: %+v, %v", w, err)
		}
		w.State[0] = 7
		p, err := s.History(ctx(), id, 1, 0)
		if err != nil || len(p) != 1 || p[0][0] != 5 {
			t.Fatalf("trail: %v, %v", p, err)
		}
		p[0][0] = 7
	}
}
