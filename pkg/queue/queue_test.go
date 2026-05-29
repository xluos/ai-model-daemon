package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/runtime"
)

// fakeRuntime is a controllable Runtime used to drive the scheduler in tests.
type fakeRuntime struct {
	kind        string
	maxParallel int

	mu          sync.Mutex
	loadedModel string
	ready       bool
	ensureErr   error
	ensureCalls int32

	inFlight atomic.Int64
}

func newFakeRuntime(kind string, maxParallel int) *fakeRuntime {
	return &fakeRuntime{kind: kind, maxParallel: maxParallel}
}

func (f *fakeRuntime) Kind() string { return f.kind }

func (f *fakeRuntime) Ensure(modelID string, opts any) error {
	atomic.AddInt32(&f.ensureCalls, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.loadedModel = modelID
	f.ready = true
	return nil
}

func (f *fakeRuntime) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadedModel = ""
	f.ready = false
	return nil
}

func (f *fakeRuntime) IsReady() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready
}

func (f *fakeRuntime) LoadedModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadedModel
}

func (f *fakeRuntime) ProxyURL() string     { return "http://127.0.0.1:9999" }
func (f *fakeRuntime) AcquireSlot()         { f.inFlight.Add(1) }
func (f *fakeRuntime) ReleaseSlot()         { f.inFlight.Add(-1) }
func (f *fakeRuntime) InFlightCount() int64 { return f.inFlight.Load() }
func (f *fakeRuntime) MaxParallel() int     { return f.maxParallel }
func (f *fakeRuntime) Logs() []string       { return nil }
func (f *fakeRuntime) Status() any          { return nil }

// newTestScheduler builds a RuntimeManager with the given fake runtimes registered
// under fresh kinds (so they don't collide with the built-in real runtimes).
func newTestScheduler(t *testing.T, cfg Config, fakes ...*fakeRuntime) *Scheduler {
	t.Helper()
	// Isolate any filesystem side effects of NewRuntimeManager.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())

	rtm := runtime.NewRuntimeManager()
	for _, f := range fakes {
		rtm.Register(f)
	}
	return NewScheduler(rtm, cfg)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxQueueSize != 100 {
		t.Errorf("MaxQueueSize = %d, want 100", cfg.MaxQueueSize)
	}
	if cfg.MaxBatchBeforeSwitch != 10 {
		t.Errorf("MaxBatchBeforeSwitch = %d, want 10", cfg.MaxBatchBeforeSwitch)
	}
	if cfg.IdleTimeout != 10*time.Minute {
		t.Errorf("IdleTimeout = %v, want 10m", cfg.IdleTimeout)
	}
}

func TestUpdateAndGetConfig(t *testing.T) {
	s := &Scheduler{config: DefaultConfig()}
	s.UpdateConfig(Config{MaxQueueSize: 5})
	if s.Config().MaxQueueSize != 5 {
		t.Errorf("UpdateConfig not applied: %d", s.Config().MaxQueueSize)
	}
}

func TestQueueInfo(t *testing.T) {
	s := &Scheduler{}
	q := []*Request{
		{ModelID: "m1"},
		{ModelID: "m1"},
		{ModelID: "m2"},
	}
	info := s.queueInfo(q, "m1", 3)
	if info.Pending != 3 {
		t.Errorf("Pending = %d, want 3", info.Pending)
	}
	if info.PendingByModel["m1"] != 2 || info.PendingByModel["m2"] != 1 {
		t.Errorf("PendingByModel = %v", info.PendingByModel)
	}
	if info.CurrentModel != "m1" {
		t.Errorf("CurrentModel = %q, want m1", info.CurrentModel)
	}
	if info.BatchServed != 3 {
		t.Errorf("BatchServed = %d, want 3", info.BatchServed)
	}
}

func TestPickNext(t *testing.T) {
	s := &Scheduler{}

	t.Run("prefers high priority matching current model", func(t *testing.T) {
		q := []*Request{
			{ID: "1", ModelID: "other", Priority: PriorityHigh},
			{ID: "2", ModelID: "current", Priority: PriorityNormal},
			{ID: "3", ModelID: "current", Priority: PriorityHigh},
		}
		if got := s.pickNext(q, "current"); got.ID != "3" {
			t.Errorf("picked %q, want 3 (high + current model)", got.ID)
		}
	})

	t.Run("prefers current model over others", func(t *testing.T) {
		q := []*Request{
			{ID: "1", ModelID: "other"},
			{ID: "2", ModelID: "current"},
		}
		if got := s.pickNext(q, "current"); got.ID != "2" {
			t.Errorf("picked %q, want 2 (current model)", got.ID)
		}
	})

	t.Run("falls back to high priority of any model", func(t *testing.T) {
		q := []*Request{
			{ID: "1", ModelID: "a", Priority: PriorityNormal},
			{ID: "2", ModelID: "b", Priority: PriorityHigh},
		}
		if got := s.pickNext(q, "current-not-present"); got.ID != "2" {
			t.Errorf("picked %q, want 2 (high priority)", got.ID)
		}
	})

	t.Run("falls back to head of queue", func(t *testing.T) {
		q := []*Request{
			{ID: "1", ModelID: "a"},
			{ID: "2", ModelID: "b"},
		}
		if got := s.pickNext(q, "current-not-present"); got.ID != "1" {
			t.Errorf("picked %q, want 1 (queue head)", got.ID)
		}
	})
}

func TestHasOtherModels(t *testing.T) {
	s := &Scheduler{}
	q := []*Request{{ModelID: "m1"}, {ModelID: "m1"}}
	if s.hasOtherModels(q, "m1") {
		t.Error("hasOtherModels should be false when all match current")
	}
	q = append(q, &Request{ModelID: "m2"})
	if !s.hasOtherModels(q, "m1") {
		t.Error("hasOtherModels should be true when a different model is queued")
	}
}

func TestEnqueueHappyPath(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := &Request{ID: "r1", ModelID: "m1", Kind: "fake", Ctx: ctx, Cancel: cancel}

	if err := s.Enqueue(req); err != nil {
		t.Fatalf("Enqueue error: %v", err)
	}
	// The runtime should have been started with our model.
	if fake.LoadedModel() != "m1" {
		t.Errorf("loaded model = %q, want m1", fake.LoadedModel())
	}
	if fake.InFlightCount() != 1 {
		t.Errorf("inFlight = %d, want 1 (slot acquired)", fake.InFlightCount())
	}

	s.Done(req)
	if fake.InFlightCount() != 0 {
		t.Errorf("inFlight after Done = %d, want 0", fake.InFlightCount())
	}
}

func TestEnqueueQueueFull(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	cfg := DefaultConfig()
	cfg.MaxQueueSize = 1
	s := newTestScheduler(t, cfg, fake)
	defer s.Close()

	// Manually wedge a request into the queue so the next Enqueue sees it full.
	s.mu.Lock()
	ks := s.getKind("fake")
	ks.queue = append(ks.queue, &Request{ID: "blocker", ModelID: "x", Kind: "fake", Ready: make(chan struct{})})
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Enqueue(&Request{ID: "r2", ModelID: "m1", Kind: "fake", Ctx: ctx, Cancel: cancel})
	if err == nil {
		t.Fatal("expected queue full error")
	}
}

func TestEnqueueModelLoadFailure(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	fake.ensureErr = context.DeadlineExceeded // any error
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Enqueue(&Request{ID: "r1", ModelID: "m1", Kind: "fake", Ctx: ctx, Cancel: cancel})
	if err == nil {
		t.Fatal("expected model load failure error")
	}
}

func TestEnqueueContextCancel(t *testing.T) {
	// A runtime that never becomes ready (Ensure blocks the model from matching).
	fake := newFakeRuntime("fake", 1)
	fake.ensureErr = nil
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	// Pre-load a different model and hold a slot so dispatch can't proceed.
	fake.Ensure("busy-model", nil)
	fake.AcquireSlot()

	ctx, cancel := context.WithCancel(context.Background())
	req := &Request{ID: "r1", ModelID: "different", Kind: "fake", Ctx: ctx, Cancel: cancel}

	done := make(chan error, 1)
	go func() { done <- s.Enqueue(req) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected context cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Enqueue did not return after context cancel")
	}
}

func TestEnqueueTimeout(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	cfg := DefaultConfig()
	cfg.MaxWaitTime = 50 * time.Millisecond
	s := newTestScheduler(t, cfg, fake)
	defer s.Close()

	// Occupy the runtime with another model + an in-flight slot so the new
	// request can never be dispatched and must hit the wait timeout.
	fake.Ensure("busy-model", nil)
	fake.AcquireSlot()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := s.Enqueue(&Request{ID: "r1", ModelID: "different", Kind: "fake", Ctx: ctx, Cancel: cancel})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestCancelQueuedRequest(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	canceled := false
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := &Request{ID: "target", ModelID: "m", Kind: "fake", Ready: make(chan struct{}), Cancel: func() { canceled = true }}

	s.mu.Lock()
	ks := s.getKind("fake")
	ks.queue = append(ks.queue, req)
	s.mu.Unlock()

	if !s.CancelQueuedRequest("target") {
		t.Error("CancelQueuedRequest should find and cancel the request")
	}
	if !canceled {
		t.Error("cancel function should have been invoked")
	}
	if s.CancelQueuedRequest("nonexistent") {
		t.Error("CancelQueuedRequest should return false for unknown ID")
	}
}

func TestProxyTarget(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	if got := s.ProxyTarget("fake"); got != "http://127.0.0.1:9999" {
		t.Errorf("ProxyTarget = %q", got)
	}
	if got := s.ProxyTarget("unknown-kind"); got != "" {
		t.Errorf("ProxyTarget(unknown) = %q, want empty", got)
	}
}

func TestStatusIncludesRegisteredKinds(t *testing.T) {
	fake := newFakeRuntime("fake", 1)
	s := newTestScheduler(t, DefaultConfig(), fake)
	defer s.Close()

	st := s.Status()
	if _, ok := st.Queues["fake"]; !ok {
		t.Error("Status should include the registered fake kind")
	}
	if _, ok := st.Queues["llm"]; !ok {
		t.Error("Status should include the built-in llm kind")
	}
}
