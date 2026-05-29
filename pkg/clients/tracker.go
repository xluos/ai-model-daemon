package clients

import (
	"sync"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/procutil"
)

type ClientInfo struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	PID           int       `json:"pid,omitempty"`
	RegisteredAt  time.Time `json:"registeredAt"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
}

type Tracker struct {
	mu             sync.Mutex
	clients        map[string]*ClientInfo
	onAllGone      func()
	gracePeriod    time.Duration
	graceTimer     *time.Timer
	cleanupTicker  *time.Ticker
	closeCh        chan struct{}
	everRegistered bool
}

func NewTracker(gracePeriod time.Duration, onAllGone func()) *Tracker {
	if gracePeriod <= 0 {
		gracePeriod = 30 * time.Second
	}
	t := &Tracker{
		clients:     make(map[string]*ClientInfo),
		onAllGone:   onAllGone,
		gracePeriod: gracePeriod,
		closeCh:     make(chan struct{}),
	}
	t.cleanupTicker = time.NewTicker(30 * time.Second)
	go t.cleanupLoop()
	return t
}

func (t *Tracker) Register(id, name string, pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.everRegistered = true
	now := time.Now()
	t.clients[id] = &ClientInfo{
		ID:            id,
		Name:          name,
		PID:           pid,
		RegisteredAt:  now,
		LastHeartbeat: now,
	}

	if t.graceTimer != nil {
		t.graceTimer.Stop()
		t.graceTimer = nil
	}
}

func (t *Tracker) Heartbeat(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.clients[id]
	if !ok {
		return false
	}
	c.LastHeartbeat = time.Now()
	return true
}

func (t *Tracker) Deregister(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.clients, id)
	remaining := len(t.clients)
	if remaining == 0 && t.everRegistered {
		t.maybeStartGrace()
	}
	return remaining
}

func (t *Tracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.clients)
}

func (t *Tracker) List() []ClientInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]ClientInfo, 0, len(t.clients))
	for _, c := range t.clients {
		result = append(result, *c)
	}
	return result
}

func (t *Tracker) Close() {
	close(t.closeCh)
	t.cleanupTicker.Stop()
	t.mu.Lock()
	if t.graceTimer != nil {
		t.graceTimer.Stop()
	}
	t.mu.Unlock()
}

func (t *Tracker) cleanupLoop() {
	for {
		select {
		case <-t.cleanupTicker.C:
			t.cleanupStale()
		case <-t.closeCh:
			return
		}
	}
}

// cleanupStale removes clients whose process is no longer alive.
// Uses PID liveness check instead of heartbeat TTL — computer sleep won't cause false evictions.
func (t *Tracker) cleanupStale() {
	t.mu.Lock()
	defer t.mu.Unlock()

	changed := false
	for id, c := range t.clients {
		if c.PID > 0 && !isProcessAlive(c.PID) {
			delete(t.clients, id)
			changed = true
		}
	}

	if changed && len(t.clients) == 0 && t.everRegistered {
		t.maybeStartGrace()
	}
}

func (t *Tracker) maybeStartGrace() {
	if t.onAllGone == nil || t.graceTimer != nil {
		return
	}
	t.graceTimer = time.AfterFunc(t.gracePeriod, func() {
		t.mu.Lock()
		count := len(t.clients)
		t.mu.Unlock()
		if count == 0 && t.onAllGone != nil {
			t.onAllGone()
		}
	})
}

func isProcessAlive(pid int) bool {
	return procutil.IsAlive(pid)
}
