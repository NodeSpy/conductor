package store

import "sort"

// GC evicts records untouched for longer than the TTL, then enforces the
// max-records LRU cap (dropping least-recently-updated first). Returns the
// number of records removed and persists the compacted state.
func (s *Store) GC() (removed int, err error) {
	s.mu.Lock()
	cutoff := s.now().Add(-s.ttl)

	// TTL eviction.
	for k, r := range s.recs {
		if s.ttl > 0 && r.UpdatedAt.Before(cutoff) {
			delete(s.recs, k)
			removed++
		}
	}

	// LRU cap.
	if s.maxPRs > 0 && len(s.recs) > s.maxPRs {
		type kv struct {
			key string
			at  int64
		}
		all := make([]kv, 0, len(s.recs))
		for k, r := range s.recs {
			all = append(all, kv{k, r.UpdatedAt.UnixNano()})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].at < all[j].at })
		for i := 0; i < len(all)-s.maxPRs; i++ {
			delete(s.recs, all[i].key)
			removed++
		}
	}
	s.mu.Unlock()

	if removed > 0 {
		if err := s.save(); err != nil {
			return removed, err
		}
	}
	return removed, nil
}
