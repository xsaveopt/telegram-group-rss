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

	added := 0
	for _, m := range msgs {
		if _, ok := s.seen[m.ID]; ok {
			continue
		}
		s.seen[m.ID] = m
		added++
	}
	if added == 0 {
		return 0
	}

	ids := make([]string, 0, len(s.seen))
	for id := range s.seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return s.seen[ids[i]].Date.After(s.seen[ids[j]].Date)
	})
	if len(ids) > s.max {
		for _, id := range ids[s.max:] {
			delete(s.seen, id)
		}
		ids = ids[:s.max]
	}
	s.order = ids
	return added
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
