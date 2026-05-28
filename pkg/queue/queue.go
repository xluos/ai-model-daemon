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
	Kind     string // "llm" or "whisper"
	Priority Priority
	ClientID string

	Ctx    context.Context
	Cancel context.CancelFunc

	Ready chan struct{} // closed when the request can be served
	Err   error        // set if the request is rejected

	enqueuedAt time.Time
}

type Status struct {
	LLMQueue     QueueInfo `json:"llmQueue"`
	WhisperQueue QueueInfo `json:"whisperQueue"`
}

type QueueInfo struct {
	Pending       int               `json:"pending"`
	PendingByModel map[string]int   `json:"pendingByModel"`
	CurrentModel  string            `json:"currentModel"`
	BatchServed   int               `json:"batchServed"`
}

type Scheduler struct {
	mu      sync.Mutex
	config  Config
	rtm     *runtime.RuntimeManager

	llmQueue     []*Request
	whisperQueue []*Request
	llmBatch     int
	whisperBatch int

	llmIdle     *time.Timer
	whisperIdle *time.Timer

	closed chan struct{}
}

func NewScheduler(rtm *runtime.RuntimeManager, cfg Config) *Scheduler {
	s := &Scheduler{
		config: cfg,
		rtm:    rtm,
		closed: make(chan struct{}),
	}
	return s
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
	return Status{
		LLMQueue:     s.queueInfo(s.llmQueue, s.rtm.LLM().LoadedModel(), s.llmBatch),
		WhisperQueue: s.queueInfo(s.whisperQueue, s.rtm.Whisper().LoadedModel(), s.whisperBatch),
	}
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

// Enqueue adds a request to the appropriate queue and blocks until the model is
// ready to serve it (or context is cancelled / max wait exceeded).
func (s *Scheduler) Enqueue(req *Request) error {
	s.mu.Lock()

	if len(s.queue(req.Kind)) >= s.config.MaxQueueSize {
		s.mu.Unlock()
		return fmt.Errorf("queue full (%d)", s.config.MaxQueueSize)
	}

	req.enqueuedAt = time.Now()
	req.Ready = make(chan struct{})

	s.appendQueue(req)
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

// Done marks a request as completed, allowing the scheduler to dispatch next.
func (s *Scheduler) Done(req *Request) {
	switch req.Kind {
	case "llm":
		s.rtm.LLM().ReleaseSlot()
	case "whisper":
		s.rtm.Whisper().ReleaseSlot()
	}
	s.resetIdleTimer(req.Kind)
	s.tryDispatch(req.Kind)
}

func (s *Scheduler) Close() {
	close(s.closed)
	s.mu.Lock()
	if s.llmIdle != nil {
		s.llmIdle.Stop()
	}
	if s.whisperIdle != nil {
		s.whisperIdle.Stop()
	}
	for _, r := range s.llmQueue {
		r.Err = fmt.Errorf("scheduler closed")
		close(r.Ready)
	}
	for _, r := range s.whisperQueue {
		r.Err = fmt.Errorf("scheduler closed")
		close(r.Ready)
	}
	s.llmQueue = nil
	s.whisperQueue = nil
	s.mu.Unlock()
}

func (s *Scheduler) tryDispatch(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	q := s.queue(kind)
	if len(q) == 0 {
		return
	}

	next := s.pickNext(q, kind)
	if next == nil {
		return
	}

	currentModel := s.currentModel(kind)
	maxParallel := s.maxParallel(kind)
	inFlight := s.inFlightCount(kind)

	if next.ModelID == currentModel && s.isRuntimeReady(kind) {
		if inFlight < int64(maxParallel) {
			s.acquireSlot(kind)
			s.removeFromQueue(next, kind)
			s.incrementBatch(kind)
			s.cancelIdleTimer(kind)
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

		batch := s.batchCount(kind)
		if currentModel != "" && batch >= s.config.MaxBatchBeforeSwitch && s.hasOtherModels(q, currentModel) {
			s.resetBatch(kind)
		}

		s.cancelIdleTimer(kind)
		go s.switchAndDispatch(kind, next.ModelID)
	}
}

func (s *Scheduler) switchAndDispatch(kind, modelID string) {
	var err error
	switch kind {
	case "llm":
		err = s.rtm.EnsureLLM(modelID, runtime.LLMOpts{})
	case "whisper":
		err = s.rtm.EnsureWhisper(modelID, runtime.WhisperOpts{})
	}

	s.mu.Lock()
	if err != nil {
		q := s.queue(kind)
		for i := 0; i < len(q); {
			if q[i].ModelID == modelID {
				q[i].Err = fmt.Errorf("model load failed: %w", err)
				close(q[i].Ready)
				q = append(q[:i], q[i+1:]...)
			} else {
				i++
			}
		}
		s.setQueue(kind, q)
		s.mu.Unlock()
		return
	}
	s.resetBatch(kind)
	s.mu.Unlock()

	s.tryDispatch(kind)
}

func (s *Scheduler) resetIdleTimer(kind string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.config.IdleTimeout <= 0 {
		return
	}

	if s.inFlightCount(kind) > 0 || len(s.queue(kind)) > 0 {
		return
	}

	timer := time.AfterFunc(s.config.IdleTimeout, func() {
		switch kind {
		case "llm":
			s.rtm.LLM().Stop()
		case "whisper":
			s.rtm.Whisper().Stop()
		}
	})

	switch kind {
	case "llm":
		if s.llmIdle != nil {
			s.llmIdle.Stop()
		}
		s.llmIdle = timer
	case "whisper":
		if s.whisperIdle != nil {
			s.whisperIdle.Stop()
		}
		s.whisperIdle = timer
	}
}

func (s *Scheduler) cancelIdleTimer(kind string) {
	switch kind {
	case "llm":
		if s.llmIdle != nil {
			s.llmIdle.Stop()
			s.llmIdle = nil
		}
	case "whisper":
		if s.whisperIdle != nil {
			s.whisperIdle.Stop()
			s.whisperIdle = nil
		}
	}
}

// --- helpers ---

func (s *Scheduler) queue(kind string) []*Request {
	switch kind {
	case "llm":
		return s.llmQueue
	case "whisper":
		return s.whisperQueue
	default:
		return nil
	}
}

func (s *Scheduler) setQueue(kind string, q []*Request) {
	switch kind {
	case "llm":
		s.llmQueue = q
	case "whisper":
		s.whisperQueue = q
	}
}

func (s *Scheduler) appendQueue(req *Request) {
	switch req.Kind {
	case "llm":
		s.llmQueue = append(s.llmQueue, req)
	case "whisper":
		s.whisperQueue = append(s.whisperQueue, req)
	}
}

func (s *Scheduler) removeRequest(req *Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeFromQueue(req, req.Kind)
}

func (s *Scheduler) removeFromQueue(req *Request, kind string) {
	q := s.queue(kind)
	for i, r := range q {
		if r == req {
			s.setQueue(kind, append(q[:i], q[i+1:]...))
			return
		}
	}
}

func (s *Scheduler) pickNext(q []*Request, kind string) *Request {
	currentModel := s.currentModel(kind)

	// First: high priority requests for current model
	for _, r := range q {
		if r.Priority == PriorityHigh && r.ModelID == currentModel {
			return r
		}
	}
	// Then: normal requests for current model
	for _, r := range q {
		if r.ModelID == currentModel {
			return r
		}
	}
	// Then: high priority for any model
	for _, r := range q {
		if r.Priority == PriorityHigh {
			return r
		}
	}
	// Then: FIFO
	return q[0]
}

func (s *Scheduler) currentModel(kind string) string {
	switch kind {
	case "llm":
		return s.rtm.LLM().LoadedModel()
	case "whisper":
		return s.rtm.Whisper().LoadedModel()
	default:
		return ""
	}
}

func (s *Scheduler) isRuntimeReady(kind string) bool {
	switch kind {
	case "llm":
		return s.rtm.LLM().IsReady()
	case "whisper":
		return s.rtm.Whisper().IsReady()
	default:
		return false
	}
}

func (s *Scheduler) inFlightCount(kind string) int64 {
	switch kind {
	case "llm":
		return s.rtm.LLM().InFlightCount()
	case "whisper":
		return s.rtm.Whisper().InFlightCount()
	default:
		return 0
	}
}

func (s *Scheduler) acquireSlot(kind string) {
	switch kind {
	case "llm":
		s.rtm.LLM().AcquireSlot()
	case "whisper":
		s.rtm.Whisper().AcquireSlot()
	}
}

func (s *Scheduler) maxParallel(kind string) int {
	switch kind {
	case "llm":
		st := s.rtm.LLM().Status()
		if st.Parallel > 0 {
			return st.Parallel
		}
		return 1
	default:
		return 1
	}
}

func (s *Scheduler) batchCount(kind string) int {
	switch kind {
	case "llm":
		return s.llmBatch
	case "whisper":
		return s.whisperBatch
	default:
		return 0
	}
}

func (s *Scheduler) incrementBatch(kind string) {
	switch kind {
	case "llm":
		s.llmBatch++
	case "whisper":
		s.whisperBatch++
	}
}

func (s *Scheduler) resetBatch(kind string) {
	switch kind {
	case "llm":
		s.llmBatch = 0
	case "whisper":
		s.whisperBatch = 0
	}
}

func (s *Scheduler) hasOtherModels(q []*Request, currentModel string) bool {
	for _, r := range q {
		if r.ModelID != currentModel {
			return true
		}
	}
	return false
}

// ProxyTarget returns the upstream URL for a request kind, or empty string if not ready.
func (s *Scheduler) ProxyTarget(kind string) string {
	switch kind {
	case "llm":
		return s.rtm.LLM().ProxyURL()
	case "whisper":
		return s.rtm.Whisper().ProxyURL()
	default:
		return ""
	}
}

// CancelQueuedRequest cancels a queued request by ID.
func (s *Scheduler) CancelQueuedRequest(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, q := range [][]*Request{s.llmQueue, s.whisperQueue} {
		for _, r := range q {
			if r.ID == requestID && r.Cancel != nil {
				r.Cancel()
				return true
			}
		}
	}
	return false
}

// HTTPHandler wraps request through the queue. The caller provides a handler
// that will be invoked once the model is loaded and ready.
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
