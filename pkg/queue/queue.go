package queue

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/xluos/ai-model-daemon/pkg/runtime"
)

type Config struct {
	MaxQueueSize         int           `json:"maxQueueSize"`
	MaxWaitTime          time.Duration `json:"maxWaitTime"`
	IdleTimeout          time.Duration `json:"idleTimeout"`
	MaxBatchBeforeSwitch int           `json:"maxBatchBeforeSwitch"`
}

func DefaultConfig() Config {
	return Config{
		MaxQueueSize:         100,
		MaxWaitTime:          5 * time.Minute,
		IdleTimeout:          10 * time.Minute,
		MaxBatchBeforeSwitch: 10,
	}
}

type Priority int

const (
	PriorityNormal Priority = 0
	PriorityHigh   Priority = 1
)

type Request struct {
	ID       string
	ModelID  string
	Kind     string
	Priority Priority
	ClientID string

	Ctx    context.Context
	Cancel context.CancelFunc

	Ready chan struct{}
	Err   error

	enqueuedAt time.Time
}

type QueueInfo struct {
	Pending        int            `json:"pending"`
	PendingByModel map[string]int `json:"pendingByModel"`
	CurrentModel   string         `json:"currentModel"`
	BatchServed    int            `json:"batchServed"`
}

type Status struct {
	Queues map[string]QueueInfo `json:"queues"`

	// Backward-compatible fields, populated from Queues.
	LLMQueue     QueueInfo `json:"llmQueue"`
	WhisperQueue QueueInfo `json:"whisperQueue"`
}

type kindState struct {
	queue     []*Request
	batch     int
	idleTimer *time.Timer
}

type Scheduler struct {
	mu     sync.Mutex
	config Config
	rtm    *runtime.RuntimeManager
	kinds  map[string]*kindState
	closed chan struct{}
}

func NewScheduler(rtm *runtime.RuntimeManager, cfg Config) *Scheduler {
	return &Scheduler{
		config: cfg,
		rtm:    rtm,
		kinds:  make(map[string]*kindState),
		closed: make(chan struct{}),
	}
}

func (s *Scheduler) getKind(kind string) *kindState {
	ks, ok := s.kinds[kind]
	if !ok {
		ks = &kindState{}
		s.kinds[kind] = ks
	}
	return ks
}

func (s *Scheduler) UpdateConfig(cfg Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = cfg
}

func (s *Scheduler) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.config
}

func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()

	queues := make(map[string]QueueInfo)
	for _, kind := range s.rtm.Kinds() {
		ks := s.getKind(kind)
		rt := s.rtm.Get(kind)
		model := ""
		if rt != nil {
			model = rt.LoadedModel()
		}
		queues[kind] = s.queueInfo(ks.queue, model, ks.batch)
	}

	st := Status{Queues: queues}
	st.LLMQueue = queues["llm"]
	st.WhisperQueue = queues["whisper"]
	return st
}

func (s *Scheduler) queueInfo(q []*Request, currentModel string, batch int) QueueInfo {
	byModel := make(map[string]int)
	for _, r := range q {
		byModel[r.ModelID]++
	}
	return QueueInfo{
		Pending:        len(q),
		PendingByModel: byModel,
		CurrentModel:   currentModel,
		BatchServed:    batch,
	}
}

func (s *Scheduler) Enqueue(req *Request) error {
	s.mu.Lock()

	ks := s.getKind(req.Kind)
	if len(ks.queue) >= s.config.MaxQueueSize {
		s.mu.Unlock()
		return fmt.Errorf("queue full (%d)", s.config.MaxQueueSize)
	}

	req.enqueuedAt = time.Now()
	req.Ready = make(chan struct{})

	ks.queue = append(ks.queue, req)
	s.mu.Unlock()

	s.tryDispatch(req.Kind)

	select {
	case <-req.Ready:
		return req.Err
	case <-req.Ctx.Done():
		s.removeRequest(req)
		return req.Ctx.Err()
	case <-time.After(s.config.MaxWaitTime):
		s.removeRequest(req)
		return fmt.Errorf("request timed out after %s", s.config.MaxWaitTime)
	case <-s.closed:
		return fmt.Errorf("scheduler closed")
	}
}

func (s *Scheduler) Done(req *Request) {
	rt := s.rtm.Get(req.Kind)
	if rt != nil {
		rt.ReleaseSlot()
	}
	s.resetIdleTimer(req.Kind)
	s.tryDispatch(req.Kind)
}

func (s *Scheduler) Close() {
	close(s.closed)
	s.mu.Lock()
	for _, ks := range s.kinds {
		if ks.idleTimer != nil {
			ks.idleTimer.Stop()
		}
		for _, r := range ks.queue {
			r.Err = fmt.Errorf("scheduler closed")
			close(r.Ready)
		}
		ks.queue = nil
	}
	s.mu.Unlock()
}

func (s *Scheduler) tryDispatch(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ks := s.getKind(kind)
	if len(ks.queue) == 0 {
		return
	}

	rt := s.rtm.Get(kind)
	if rt == nil {
		return
	}

	next := s.pickNext(ks.queue, rt.LoadedModel())
	if next == nil {
		return
	}

	currentModel := rt.LoadedModel()
	maxParallel := rt.MaxParallel()
	inFlight := rt.InFlightCount()

	if next.ModelID == currentModel && rt.IsReady() {
		if inFlight < int64(maxParallel) {
			rt.AcquireSlot()
			s.removeFromQueue(next, kind)
			ks.batch++
			s.cancelIdleTimerLocked(kind)
			close(next.Ready)
			if inFlight+1 < int64(maxParallel) {
				go s.tryDispatch(kind)
			}
			return
		}
		return
	}

	if next.ModelID != currentModel {
		if inFlight > 0 {
			return
		}

		if currentModel != "" && ks.batch >= s.config.MaxBatchBeforeSwitch && s.hasOtherModels(ks.queue, currentModel) {
			ks.batch = 0
		}

		s.cancelIdleTimerLocked(kind)
		go s.switchAndDispatch(kind, next.ModelID)
	}
}

func (s *Scheduler) switchAndDispatch(kind, modelID string) {
	err := s.rtm.Ensure(kind, modelID, nil)

	s.mu.Lock()
	if err != nil {
		ks := s.getKind(kind)
		q := ks.queue
		for i := 0; i < len(q); {
			if q[i].ModelID == modelID {
				q[i].Err = fmt.Errorf("model load failed: %w", err)
				close(q[i].Ready)
				q = append(q[:i], q[i+1:]...)
			} else {
				i++
			}
		}
		ks.queue = q
		s.mu.Unlock()
		return
	}
	s.getKind(kind).batch = 0
	s.mu.Unlock()

	s.tryDispatch(kind)
}

func (s *Scheduler) resetIdleTimer(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.IdleTimeout <= 0 {
		return
	}

	rt := s.rtm.Get(kind)
	if rt == nil {
		return
	}

	ks := s.getKind(kind)
	if rt.InFlightCount() > 0 || len(ks.queue) > 0 {
		return
	}

	timer := time.AfterFunc(s.config.IdleTimeout, func() {
		if r := s.rtm.Get(kind); r != nil {
			r.Stop()
		}
	})

	if ks.idleTimer != nil {
		ks.idleTimer.Stop()
	}
	ks.idleTimer = timer
}

func (s *Scheduler) cancelIdleTimerLocked(kind string) {
	ks := s.getKind(kind)
	if ks.idleTimer != nil {
		ks.idleTimer.Stop()
		ks.idleTimer = nil
	}
}

// --- helpers ---

func (s *Scheduler) removeRequest(req *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeFromQueue(req, req.Kind)
}

func (s *Scheduler) removeFromQueue(req *Request, kind string) {
	ks := s.getKind(kind)
	for i, r := range ks.queue {
		if r == req {
			ks.queue = append(ks.queue[:i], ks.queue[i+1:]...)
			return
		}
	}
}

func (s *Scheduler) pickNext(q []*Request, currentModel string) *Request {
	for _, r := range q {
		if r.Priority == PriorityHigh && r.ModelID == currentModel {
			return r
		}
	}
	for _, r := range q {
		if r.ModelID == currentModel {
			return r
		}
	}
	for _, r := range q {
		if r.Priority == PriorityHigh {
			return r
		}
	}
	return q[0]
}

func (s *Scheduler) hasOtherModels(q []*Request, currentModel string) bool {
	for _, r := range q {
		if r.ModelID != currentModel {
			return true
		}
	}
	return false
}

func (s *Scheduler) ProxyTarget(kind string) string {
	rt := s.rtm.Get(kind)
	if rt == nil {
		return ""
	}
	return rt.ProxyURL()
}

func (s *Scheduler) CancelQueuedRequest(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ks := range s.kinds {
		for _, r := range ks.queue {
			if r.ID == requestID && r.Cancel != nil {
				r.Cancel()
				return true
			}
		}
	}
	return false
}

func (s *Scheduler) HTTPHandler(kind, modelID, clientID string, priority Priority, w http.ResponseWriter, r *http.Request, serve func()) error {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	req := &Request{
		ID:       fmt.Sprintf("%s-%d", kind, time.Now().UnixNano()),
		ModelID:  modelID,
		Kind:     kind,
		Priority: priority,
		ClientID: clientID,
		Ctx:      ctx,
		Cancel:   cancel,
	}

	if err := s.Enqueue(req); err != nil {
		return err
	}
	defer s.Done(req)

	serve()
	return nil
}
