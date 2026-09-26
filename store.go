package main

import (
	"sort"
	"sync"
)

type Store struct {
	mu    sync.RWMutex
	max   int
	seen  map[string]Message
	order []string
}

func NewStore(max int) *Store {
	return &Store{max: max, seen: make(map[string]Message)}
}

func (s *Store) Add(msgs []Message) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var added []string
	for _, m := range msgs {
		if _, ok := s.seen[m.ID]; ok {
			continue
		}
		s.seen[m.ID] = m
		added = append(added, m.ID)
	}
	if len(added) == 0 {
		return 0
	}

	ids := make([]string, 0, len(s.seen))
	ids = append(ids, s.order...)
	ids = append(ids, added...)
	sort.SliceStable(ids, func(i, j int) bool {
		return s.seen[ids[i]].Date.After(s.seen[ids[j]].Date)
	})
	if len(ids) > s.max {
		for _, id := range ids[s.max:] {
			delete(s.seen, id)
		}
		ids = ids[:s.max]
	}
	s.order = ids
	kept := 0
	for _, id := range added {
		if _, ok := s.seen[id]; ok {
			kept++
		}
	}
	return kept
}

func (s *Store) List() []Message {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Message, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.seen[id])
	}
	return out
}
