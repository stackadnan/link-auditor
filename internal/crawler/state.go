package crawler

import "sync"

// State provides thread-safe tracking of which normalized URLs have already
// been queued or visited during a scan, ensuring the same URL is only
// requested once per run regardless of how many pages link to it. It also
// enforces the scan's page budget (see NewState), so admission control and
// deduplication share a single lock instead of racing against each other.
type State struct {
	mu       sync.Mutex
	visited  map[string]struct{}
	maxPages int // 0 means unlimited
	limited  bool
}

// NewState returns an initialized, empty State. maxPages caps how many
// distinct URLs MarkVisited will admit; 0 means unlimited.
func NewState(maxPages int) *State {
	return &State{visited: make(map[string]struct{}), maxPages: maxPages}
}

// MarkVisited atomically checks whether normalizedURL has already been
// recorded and, if not, admits it. It returns true only the first time a
// given URL is marked *and* the scan's page budget (see NewState) has not
// yet been exhausted; callers use that to decide whether the URL should be
// enqueued for crawling. normalizedURL is expected to already be in
// canonical form (see NormalizeURL) so that equivalent URLs collide on the
// same map key.
//
// Once the budget is exhausted, MarkVisited keeps rejecting new URLs (they
// are never added to the visited set) and LimitReached starts returning
// true. A URL that arrives after the budget was already hit is simply
// dropped, not queued for "later": the crawl is bounded, not paused.
func (s *State) MarkVisited(normalizedURL string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, seen := s.visited[normalizedURL]; seen {
		return false
	}
	if s.maxPages > 0 && len(s.visited) >= s.maxPages {
		s.limited = true
		return false
	}
	s.visited[normalizedURL] = struct{}{}
	return true
}

// LimitReached reports whether the page budget passed to NewState caused at
// least one discovered URL to be rejected by MarkVisited.
func (s *State) LimitReached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.limited
}

// Count returns the number of unique URLs recorded so far.
func (s *State) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.visited)
}
