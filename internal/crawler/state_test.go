package crawler

import (
	"sync"
	"testing"
)

func TestState_MarkVisited_Deduplicates(t *testing.T) {
	tests := []struct {
		name  string
		urls  []string
		wantN int // expected number of URLs for which MarkVisited returns true
	}{
		{
			name:  "all unique",
			urls:  []string{"https://a.com/", "https://b.com/", "https://c.com/"},
			wantN: 3,
		},
		{
			name:  "all duplicates",
			urls:  []string{"https://a.com/", "https://a.com/", "https://a.com/"},
			wantN: 1,
		},
		{
			name:  "mixed",
			urls:  []string{"https://a.com/", "https://b.com/", "https://a.com/", "https://c.com/", "https://b.com/"},
			wantN: 3,
		},
		{
			name:  "empty",
			urls:  nil,
			wantN: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewState()
			newlyMarked := 0
			for _, u := range tt.urls {
				if s.MarkVisited(u) {
					newlyMarked++
				}
			}
			if newlyMarked != tt.wantN {
				t.Errorf("newly marked = %d, want %d", newlyMarked, tt.wantN)
			}
			if got := s.Count(); got != tt.wantN {
				t.Errorf("Count() = %d, want %d", got, tt.wantN)
			}
		})
	}
}

func TestState_MarkVisited_FirstCallerWins(t *testing.T) {
	s := NewState()
	if !s.MarkVisited("https://example.com/") {
		t.Fatal("first MarkVisited call should return true")
	}
	if s.MarkVisited("https://example.com/") {
		t.Fatal("second MarkVisited call for the same URL should return false")
	}
}

// TestState_MarkVisited_ConcurrentSafe exercises the mutex-protected map
// under the race detector (run via `go test -race`) with many goroutines
// racing to mark an overlapping set of URLs. Exactly one caller per URL
// must observe true.
func TestState_MarkVisited_ConcurrentSafe(t *testing.T) {
	const (
		workers   = 50
		uniqueURL = 20
	)

	s := NewState()
	var wins [uniqueURL]int32
	var mu sync.Mutex // guards wins from concurrent increment races in the test itself

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for u := 0; u < uniqueURL; u++ {
				url := urlFor(u)
				if s.MarkVisited(url) {
					mu.Lock()
					wins[u]++
					mu.Unlock()
				}
			}
		}(i)
	}
	wg.Wait()

	for u, count := range wins {
		if count != 1 {
			t.Errorf("URL %d was marked as newly-visited %d times, want exactly 1", u, count)
		}
	}
	if got := s.Count(); got != uniqueURL {
		t.Errorf("Count() = %d, want %d", got, uniqueURL)
	}
}

func urlFor(i int) string {
	return "https://example.com/page/" + string(rune('a'+i))
}
