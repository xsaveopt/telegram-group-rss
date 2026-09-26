package main

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

var baseTime = time.Date(2024, time.May, 1, 12, 0, 0, 0, time.UTC)

func msg(id string, minutes int) Message {
	return Message{
		ID:        id,
		Channel:   "examplechan",
		PostID:    id,
		PlainText: "message " + id,
		Date:      baseTime.Add(time.Duration(minutes) * time.Minute),
	}
}

func ids(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewStore(t *testing.T) {
	s := NewStore(10)
	if s.max != 10 {
		t.Errorf("max = %d, want 10", s.max)
	}
	if s.seen == nil {
		t.Error("seen map is nil")
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("List on a fresh store = %v, want empty", got)
	}
}

func TestStoreAddDedup(t *testing.T) {
	s := NewStore(10)

	if n := s.Add([]Message{msg("1", 1), msg("2", 2)}); n != 2 {
		t.Fatalf("first Add = %d, want 2", n)
	}
	if n := s.Add([]Message{msg("1", 1), msg("2", 2)}); n != 0 {
		t.Errorf("re-Add of the same ids = %d, want 0", n)
	}
	if n := s.Add([]Message{msg("2", 2), msg("3", 3)}); n != 1 {
		t.Errorf("Add with one new id = %d, want 1", n)
	}
	if n := s.Add(nil); n != 0 {
		t.Errorf("Add(nil) = %d, want 0", n)
	}

	got := ids(s.List())
	want := []string{"3", "2", "1"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreAddDuplicatesWithinOneBatch(t *testing.T) {
	s := NewStore(10)
	if n := s.Add([]Message{msg("1", 1), msg("1", 1), msg("2", 2)}); n != 2 {
		t.Fatalf("Add = %d, want 2", n)
	}
	if got := len(s.List()); got != 2 {
		t.Errorf("List length = %d, want 2", got)
	}
}

func TestStoreListNewestFirst(t *testing.T) {
	s := NewStore(10)
	s.Add([]Message{msg("old", 0), msg("new", 30), msg("mid", 15)})

	got := ids(s.List())
	want := []string{"new", "mid", "old"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreRingCap(t *testing.T) {
	s := NewStore(3)

	if n := s.Add([]Message{msg("1", 1), msg("2", 2), msg("3", 3), msg("4", 4), msg("5", 5)}); n != 3 {
		t.Errorf("Add = %d, want 3, the number of new messages the store kept", n)
	}

	got := ids(s.List())
	want := []string{"5", "4", "3"}
	if !equalStrings(got, want) {
		t.Fatalf("List = %v, want %v", got, want)
	}

	if len(s.seen) != 3 {
		t.Errorf("seen holds %d entries, want 3", len(s.seen))
	}
	for _, id := range []string{"1", "2"} {
		if _, ok := s.seen[id]; ok {
			t.Errorf("evicted id %q is still in seen", id)
		}
	}

	if n := s.Add([]Message{msg("6", 6)}); n != 1 {
		t.Fatalf("Add after eviction = %d, want 1", n)
	}
	got = ids(s.List())
	want = []string{"6", "5", "4"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreEvictedIDIsNotCountedAgain(t *testing.T) {
	s := NewStore(2)
	s.Add([]Message{msg("1", 1), msg("2", 2), msg("3", 3)})

	for round := range 3 {
		if n := s.Add([]Message{msg("1", 1)}); n != 0 {
			t.Errorf("round %d: re-Add of an evicted id that is still too old = %d, want 0", round, n)
		}
	}
	got := ids(s.List())
	want := []string{"3", "2"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreConcurrentAddAndList(t *testing.T) {
	const (
		writers = 8
		batches = 25
		perAdd  = 4
	)
	s := NewStore(50)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for b := range batches {
				batch := make([]Message, 0, perAdd)
				for i := range perAdd {
					id := fmt.Sprintf("w%d-b%d-i%d", w, b, i)
					batch = append(batch, msg(id, b*perAdd+i))
				}
				s.Add(batch)
			}
		}(w)
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range batches * perAdd {
				for _, m := range s.List() {
					_ = m.ID
				}
			}
		}()
	}
	wg.Wait()

	if got := len(s.List()); got != 50 {
		t.Errorf("List length = %d, want 50", got)
	}
	if got := len(s.seen); got != 50 {
		t.Errorf("seen length = %d, want 50", got)
	}
}

func TestStoreZeroDatesSortLast(t *testing.T) {
	s := NewStore(10)
	undated := Message{ID: "undated", Channel: "examplechan", PostID: "undated"}
	if n := s.Add([]Message{undated, msg("1", 1), msg("2", 2)}); n != 3 {
		t.Fatalf("Add = %d, want 3", n)
	}
	got := ids(s.List())
	want := []string{"2", "1", "undated"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreFullStoreDoesNotCountOlderMessages(t *testing.T) {
	s := NewStore(3)
	s.Add([]Message{msg("1", 1), msg("2", 2), msg("3", 3)})

	undated := Message{ID: "undated", Channel: "examplechan", PostID: "undated"}
	older := msg("0", 0)
	for round := range 3 {
		if n := s.Add([]Message{undated, older}); n != 0 {
			t.Errorf("round %d: Add of messages older than a full store = %d, want 0", round, n)
		}
		got := ids(s.List())
		want := []string{"3", "2", "1"}
		if !equalStrings(got, want) {
			t.Errorf("round %d: List = %v, want %v", round, got, want)
		}
		if len(s.seen) != 3 {
			t.Errorf("round %d: seen holds %d entries, want 3", round, len(s.seen))
		}
	}

	if n := s.Add([]Message{older, msg("4", 4)}); n != 1 {
		t.Errorf("Add with one newer message = %d, want 1", n)
	}
	got := ids(s.List())
	want := []string{"4", "3", "2"}
	if !equalStrings(got, want) {
		t.Errorf("List = %v, want %v", got, want)
	}
}

func TestStoreEqualDatesOrderIsDeterministic(t *testing.T) {
	batch := []Message{msg("a", 5), msg("b", 5), msg("c", 5), msg("d", 5), msg("e", 5), msg("f", 5)}

	var first []string
	for trial := range 50 {
		s := NewStore(10)
		s.Add(batch)
		got := ids(s.List())
		if trial == 0 {
			first = got
			continue
		}
		if !equalStrings(got, first) {
			t.Fatalf("trial %d: List = %v, first trial gave %v", trial, got, first)
		}
	}
}

func TestStoreEqualDatesEvictionIsDeterministic(t *testing.T) {
	batch := []Message{msg("a", 5), msg("b", 5), msg("c", 5), msg("d", 5), msg("e", 5), msg("f", 5)}

	var first []string
	for trial := range 50 {
		s := NewStore(3)
		s.Add(batch)
		got := ids(s.List())
		if len(got) != 3 {
			t.Fatalf("trial %d: List length = %d, want 3", trial, len(got))
		}
		if trial == 0 {
			first = got
			continue
		}
		if !equalStrings(got, first) {
			t.Fatalf("trial %d: kept %v, first trial kept %v", trial, got, first)
		}
	}
}

func TestStoreEqualDatesOrderIsStableAcrossAdds(t *testing.T) {
	s := NewStore(20)
	s.Add([]Message{msg("a", 5), msg("b", 5), msg("c", 5), msg("d", 5), msg("e", 5)})
	before := ids(s.List())

	for i := range 10 {
		s.Add([]Message{msg(fmt.Sprintf("old%d", i), -i-1)})
		got := ids(s.List())[:5]
		if !equalStrings(got, before) {
			t.Fatalf("after Add %d the equal-dated messages reordered to %v, were %v", i, got, before)
		}
	}
}
