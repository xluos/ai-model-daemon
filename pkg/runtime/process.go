package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

type ProcessState string

const (
	StateIdle     ProcessState = "idle"
	StateStarting ProcessState = "starting"
	StateReady    ProcessState = "ready"
	StateStopping ProcessState = "stopping"
	StateError    ProcessState = "error"
)

type ProcessConfig struct {
	Binary    string
	Args      []string
	Env       []string
	HealthURL string
	HealthTimeout  time.Duration
	HealthInterval time.Duration
	StopTimeout    time.Duration
}

func (c *ProcessConfig) defaults() {
	if c.HealthTimeout == 0 {
		c.HealthTimeout = 60 * time.Second
	}
	if c.HealthInterval == 0 {
		c.HealthInterval = 500 * time.Millisecond
	}
	if c.StopTimeout == 0 {
		c.StopTimeout = 5 * time.Second
	}
}

type ProcessHandle struct {
	mu     sync.Mutex
	state  ProcessState
	cmd    *exec.Cmd
	cancel context.CancelFunc
	pid    int
	err    error
	exitCh chan struct{}

	logBuf  *ringBuffer
	onCrash func(error)
}

func NewProcess(onCrash func(error)) *ProcessHandle {
	return &ProcessHandle{
		state:   StateIdle,
		logBuf:  newRingBuffer(200),
		onCrash: onCrash,
	}
}

func (p *ProcessHandle) State() ProcessState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *ProcessHandle) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pid
}

func (p *ProcessHandle) Error() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

func (p *ProcessHandle) Logs() []string {
	return p.logBuf.Lines()
}

func (p *ProcessHandle) Start(cfg ProcessConfig) error {
	p.mu.Lock()
	if p.state != StateIdle && p.state != StateError {
		p.mu.Unlock()
		return fmt.Errorf("cannot start: state is %s", p.state)
	}
	p.state = StateStarting
	p.err = nil
	p.logBuf.Reset()
	p.mu.Unlock()

	cfg.defaults()

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, cfg.Binary, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		p.setError(fmt.Errorf("stdout pipe: %w", err))
		return p.err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		p.setError(fmt.Errorf("stderr pipe: %w", err))
		return p.err
	}

	if err := cmd.Start(); err != nil {
		cancel()
		p.setError(fmt.Errorf("start: %w", err))
		return p.err
	}

	exitCh := make(chan struct{})

	p.mu.Lock()
	p.cmd = cmd
	p.cancel = cancel
	p.pid = cmd.Process.Pid
	p.exitCh = exitCh
	p.mu.Unlock()

	var drainWg sync.WaitGroup
	drainWg.Add(2)
	go func() { defer drainWg.Done(); p.drainReader(stdout) }()
	go func() { defer drainWg.Done(); p.drainReader(stderr) }()

	go p.watchExit(&drainWg, exitCh)

	if cfg.HealthURL != "" {
		if err := p.waitHealth(cfg.HealthURL, cfg.HealthTimeout, cfg.HealthInterval); err != nil {
			p.Stop(cfg.StopTimeout)
			drainWg.Wait()
			tail := p.lastLogLines(20)
			if tail != "" {
				p.setError(fmt.Errorf("health check failed: %w\n--- process output ---\n%s", err, tail))
			} else {
				p.setError(fmt.Errorf("health check failed: %w", err))
			}
			return p.err
		}
	}

	p.mu.Lock()
	if p.state == StateStarting {
		p.state = StateReady
	}
	p.mu.Unlock()
	return nil
}

func (p *ProcessHandle) Stop(timeout time.Duration) error {
	p.mu.Lock()
	if p.state == StateIdle {
		p.mu.Unlock()
		return nil
	}
	if p.cmd == nil || p.cmd.Process == nil {
		p.state = StateIdle
		p.pid = 0
		p.mu.Unlock()
		return nil
	}
	p.state = StateStopping
	cmd := p.cmd
	cancel := p.cancel
	exitCh := p.exitCh
	p.mu.Unlock()

	cmd.Process.Signal(os.Interrupt)

	if timeout == 0 {
		timeout = 5 * time.Second
	}

	select {
	case <-exitCh:
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-exitCh
	}

	if cancel != nil {
		cancel()
	}

	p.mu.Lock()
	p.state = StateIdle
	p.cmd = nil
	p.cancel = nil
	p.pid = 0
	p.mu.Unlock()

	return nil
}

func (p *ProcessHandle) setError(err error) {
	p.mu.Lock()
	p.state = StateError
	p.err = err
	p.cmd = nil
	p.cancel = nil
	p.pid = 0
	p.mu.Unlock()
}

func (p *ProcessHandle) watchExit(drainWg *sync.WaitGroup, exitCh chan struct{}) {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd == nil {
		close(exitCh)
		return
	}

	err := cmd.Wait()
	drainWg.Wait()
	close(exitCh)

	p.mu.Lock()
	wasStopping := p.state == StateStopping
	if !wasStopping && p.state != StateIdle {
		p.state = StateError
		tail := p.lastLogLinesLocked(10)
		if tail != "" {
			p.err = fmt.Errorf("process exited unexpectedly: %w\n%s", err, tail)
		} else {
			p.err = fmt.Errorf("process exited unexpectedly: %w", err)
		}
		p.cmd = nil
		p.cancel = nil
		p.pid = 0
	}
	p.mu.Unlock()

	if !wasStopping && p.onCrash != nil {
		p.onCrash(err)
	}
}

func (p *ProcessHandle) waitHealth(url string, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		p.mu.Lock()
		s := p.state
		p.mu.Unlock()
		if s != StateStarting {
			return fmt.Errorf("process exited during health check")
		}

		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(interval)
	}
	return fmt.Errorf("health check timed out after %s", timeout)
}

func (p *ProcessHandle) lastLogLines(n int) string {
	lines := p.logBuf.Lines()
	if len(lines) == 0 {
		return ""
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	result := ""
	for _, l := range lines[start:] {
		result += l + "\n"
	}
	return result
}

func (p *ProcessHandle) lastLogLinesLocked(n int) string {
	lines := p.logBuf.LinesNoLock()
	if len(lines) == 0 {
		return ""
	}
	start := len(lines) - n
	if start < 0 {
		start = 0
	}
	result := ""
	for _, l := range lines[start:] {
		result += l + "\n"
	}
	return result
}

func (p *ProcessHandle) drainReader(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 256*1024)
	for scanner.Scan() {
		p.logBuf.Add(scanner.Text())
	}
}

// ringBuffer is a fixed-size circular buffer of log lines.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	pos   int
	full  bool
	cap   int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{
		lines: make([]string, capacity),
		cap:   capacity,
	}
}

func (rb *ringBuffer) Add(line string) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.lines[rb.pos] = line
	rb.pos++
	if rb.pos >= rb.cap {
		rb.pos = 0
		rb.full = true
	}
}

func (rb *ringBuffer) Lines() []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.linesNoLock()
}

func (rb *ringBuffer) LinesNoLock() []string {
	return rb.linesNoLock()
}

func (rb *ringBuffer) linesNoLock() []string {
	if !rb.full {
		result := make([]string, rb.pos)
		copy(result, rb.lines[:rb.pos])
		return result
	}
	result := make([]string, rb.cap)
	copy(result, rb.lines[rb.pos:])
	copy(result[rb.cap-rb.pos:], rb.lines[:rb.pos])
	return result
}

func (rb *ringBuffer) Reset() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.pos = 0
	rb.full = false
}
