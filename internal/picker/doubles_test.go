package picker

import (
	"context"
	"sync"

	"github.com/fm39hz/gotomux/internal/store"
	"github.com/fm39hz/gotomux/internal/tmux"
)

// countingStore implements just enough of store.Storer for picker tests and
// counts every call, so a test can assert that a path performs no store I/O.
//
// store.Storer has 32 methods. Embedding it as a nil interface means any method
// this double does not implement panics instead of silently returning a zero
// value — which is what a "this path must not touch the store" test wants: a
// loud failure, not a passing test over an unnoticed call.
type countingStore struct {
	store.Storer

	mu      sync.Mutex
	presets []store.PresetMeta
	usage   map[string]store.Usage
	pairs   map[string]int64
	zox     []store.ZoxRow

	listMeta   int
	allUsage   int
	pairScores int
	loadZox    int
	stickyID   int
}

func (s *countingStore) ListMeta() ([]store.PresetMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listMeta++
	return s.presets, nil
}

func (s *countingStore) AllUsage() (map[string]store.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allUsage++
	return s.usage, nil
}

func (s *countingStore) PairScores(session string, now int64) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairScores++
	return s.pairs, nil
}

func (s *countingStore) LoadZox() ([]store.ZoxRow, int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadZox++
	if len(s.zox) == 0 {
		return nil, 0, false
	}
	return s.zox, 0, true
}

func (s *countingStore) SaveZox(rows []store.ZoxRow) error { return nil }

// StickyID returning "" short-circuits template.StickyLabel to "default" without
// touching the shape tables.
func (s *countingStore) StickyID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stickyID++
	return ""
}

func (s *countingStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listMeta + s.allUsage + s.pairScores + s.loadZox + s.stickyID
}

// countingConnector implements the tmux calls the picker's construction path can
// reach, counting them. Anything else panics, for the same reason as above.
type countingConnector struct {
	tmux.Connector

	mu      sync.Mutex
	live    []tmux.LiveSession
	session string
	path    string

	listLive       int
	currentSession int
	currentPath    int
}

func (c *countingConnector) ListLive(ctx context.Context) ([]tmux.LiveSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listLive++
	return c.live, nil
}

func (c *countingConnector) CurrentSession(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentSession++
	return c.session
}

func (c *countingConnector) CurrentSessionPath(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentPath++
	return c.path
}

func (c *countingConnector) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLive + c.currentSession + c.currentPath
}
