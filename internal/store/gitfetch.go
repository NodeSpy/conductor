package store

import "encoding/json"

// Git fetch state (engine/gitfetch.go), conductor's own — never in a
// sandbox's view, never a credential: git-fetch.json, repo → the connector
// instance whose git fetch credential last covered trusted work on it, so
// a hand-off right after a restart mints through the same instance.

// PutGitFetchInstance records repo's instance; persists only on a change.
func (s *Store) PutGitFetchInstance(repo, instance string) error {
	s.mu.Lock()
	if s.gitFetch[repo] == instance {
		s.mu.Unlock()
		return nil
	}
	s.gitFetch[repo] = instance
	s.mu.Unlock()
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.gitFetch, "", "  ")
		return b, s.gitFetchPath, err
	})
}

// GitFetchInstance is repo's recorded instance.
func (s *Store) GitFetchInstance(repo string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.gitFetch[repo]
	return v, ok
}
