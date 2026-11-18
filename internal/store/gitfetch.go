package store

import "encoding/json"

// Git fetch instances: repo (owner/name) → the connector instance whose git
// fetch credential last covered trusted work on it (engine/gitfetch.go).
// Persisted in git-fetch.json beside runs.json so a cache warm right after
// a restart or auto-update mints through the same instance it did before.
// The map holds names only — never a credential.

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
