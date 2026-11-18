package store

import (
	"encoding/json"
	"slices"
	"sort"
)

// Git fetch state (engine/gitfetch.go), conductor's own — never in a
// sandbox's view, never a credential:
//
//   - git-fetch.json: repo → the connector instance whose git fetch
//     credential last covered trusted work on it, so a cache warm right
//     after a restart mints through the same instance.
//   - git-learned.json: (repo, instance) → per cache recipe, the
//     repositories ("host/path") the latest successful default-branch warm
//     of that recipe fetched with the credential — the repo's learned
//     dependencies, the scope every other launch's credential gets.

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

func learnedKey(repo, instance string) string { return repo + "\x00" + instance }

// PutGitLearned replaces what recipe's latest warm of repo learned through
// instance: a dependency it no longer fetched drops off.
func (s *Store) PutGitLearned(repo, instance, recipe string, fetched []string) error {
	fetched = slices.Clone(fetched)
	sort.Strings(fetched)
	fetched = slices.Compact(fetched)
	k := learnedKey(repo, instance)
	s.mu.Lock()
	cur, has := s.learned[k][recipe]
	if (!has && len(fetched) == 0) || (has && slices.Equal(cur, fetched)) {
		s.mu.Unlock()
		return nil
	}
	if s.learned[k] == nil {
		s.learned[k] = map[string][]string{}
	}
	if len(fetched) == 0 {
		delete(s.learned[k], recipe)
		if len(s.learned[k]) == 0 {
			delete(s.learned, k)
		}
	} else {
		s.learned[k][recipe] = fetched
	}
	s.mu.Unlock()
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.learned, "", "  ")
		return b, s.learnedPath, err
	})
}

// GitLearned is repo's learned dependencies through instance: every
// recipe's, sorted.
func (s *Store) GitLearned(repo, instance string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, l := range s.learned[learnedKey(repo, instance)] {
		out = append(out, l...)
	}
	sort.Strings(out)
	return slices.Compact(out)
}
