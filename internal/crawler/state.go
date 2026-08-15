package crawler

import "sync"

// State provides thread-safe tracking of which normalized URLs have already
// been queued or visited during a scan, ensuring the same URL is only
// requested once per run regardless of how many pages link to it.
type State struct {
	mu      sync.Mutex
	visited map[string]struct{}
}

// NewState returns an initialized, empty State.
func NewState() *State {
	return &State{visited: make(map[string]struct{})}
}

// MarkVisited atomically checks whether normalizedURL has already been
// recorded and, if not, records it. It returns true only the first time a
// given URL is marked; callers use that to decide whether the URL should be
// enqueued for crawling. normalizedURL is expected to already be in
// canonical form (see NormalizeURL) so that equivalent URLs collide on the
// same map key.
func (s *State) MarkVisited(normalizedURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, seen := s.visited[normalizedURL]; seen {
		return false
	}
	s.visited[normalizedURL] = struct{}{}
	return true
}

// Count returns the number of unique URLs recorded so far.
func (s *State) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.visited)
}
