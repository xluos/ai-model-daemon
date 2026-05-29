package clients

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestRegisterAndCount(t *testing.T) {
	tr := NewTracker(time.Minute, nil)
	defer tr.Close()

	if tr.Count() != 0 {
		t.Errorf("new tracker count = %d, want 0", tr.Count())
	}

	tr.Register("a", "AppA", os.Getpid())
	tr.Register("b", "AppB", os.Getpid())
	if tr.Count() != 2 {
		t.Errorf("count after 2 registers = %d, want 2", tr.Count())
	}

	// Re-registering the same ID overwrites, not duplicates.
	tr.Register("a", "AppA-renamed", os.Getpid())
	if tr.Count() != 2 {
		t.Errorf("count after re-register = %d, want 2", tr.Count())
	}
}

func TestDeregisterReturnsRemaining(t *testing.T) {
	tr := NewTracker(time.Minute, nil)
	defer tr.Close()

	tr.Register("a", "", os.Getpid())
	tr.Register("b", "", os.Getpid())

	if remaining := tr.Deregister("a"); remaining != 1 {
		t.Errorf("Deregister returned %d, want 1", remaining)
	}
	if remaining := tr.Deregister("b"); remaining != 0 {
		t.Errorf("Deregister returned %d, want 0", remaining)
	}
	// Deregistering an unknown ID is a no-op.
	if remaining := tr.Deregister("ghost"); remaining != 0 {
		t.Errorf("Deregister(ghost) returned %d, want 0", remaining)
	}
}

func TestHeartbeat(t *testing.T) {
	tr := NewTracker(time.Minute, nil)
	defer tr.Close()

	tr.Register("a", "", os.Getpid())
	before := tr.List()[0].LastHeartbeat

	time.Sleep(5 * time.Millisecond)
	if !tr.Heartbeat("a") {
		t.Error("Heartbeat on existing client should return true")
	}
	if tr.Heartbeat("missing") {
		t.Error("Heartbeat on missing client should return false")
	}

	after := tr.List()[0].LastHeartbeat
	if !after.After(before) {
		t.Error("Heartbeat should update LastHeartbeat")
	}
}

func TestList(t *testing.T) {
	tr := NewTracker(time.Minute, nil)
	defer tr.Close()

	tr.Register("a", "AppA", 123)
	list := tr.List()
	if len(list) != 1 {
		t.Fatalf("List len = %d, want 1", len(list))
	}
	if list[0].ID != "a" || list[0].Name != "AppA" || list[0].PID != 123 {
		t.Errorf("unexpected client info: %+v", list[0])
	}
}

func TestGraceCallbackFiresWhenAllGone(t *testing.T) {
	var mu sync.Mutex
	called := false
	tr := NewTracker(40*time.Millisecond, func() {
		mu.Lock()
		called = true
		mu.Unlock()
	})
	defer tr.Close()

	tr.Register("a", "", os.Getpid())
	tr.Deregister("a")

	// Grace timer should fire after ~40ms.
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := called
	mu.Unlock()
	if !got {
		t.Error("onAllGone should fire after grace period when all clients leave")
	}
}

func TestGraceCallbackCanceledByReregister(t *testing.T) {
	var mu sync.Mutex
	called := false
	tr := NewTracker(60*time.Millisecond, func() {
		mu.Lock()
		called = true
		mu.Unlock()
	})
	defer tr.Close()

	tr.Register("a", "", os.Getpid())
	tr.Deregister("a") // starts grace timer
	time.Sleep(20 * time.Millisecond)
	tr.Register("b", "", os.Getpid()) // should cancel the grace timer

	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	got := called
	mu.Unlock()
	if got {
		t.Error("onAllGone must NOT fire if a client re-registered during grace")
	}
}

func TestGraceNotStartedBeforeAnyRegister(t *testing.T) {
	var mu sync.Mutex
	called := false
	tr := NewTracker(30*time.Millisecond, func() {
		mu.Lock()
		called = true
		mu.Unlock()
	})
	defer tr.Close()

	// Deregister without ever registering — everRegistered is false.
	tr.Deregister("never-existed")
	time.Sleep(80 * time.Millisecond)
	mu.Lock()
	got := called
	mu.Unlock()
	if got {
		t.Error("onAllGone must not fire if no client ever registered")
	}
}

func TestIsProcessAlive(t *testing.T) {
	if !isProcessAlive(os.Getpid()) {
		t.Error("current process should be alive")
	}
	// PID 0 / clearly invalid PIDs should not be reported alive.
	if isProcessAlive(-1) {
		t.Error("negative PID should not be alive")
	}
}

func TestCloseIsSafe(t *testing.T) {
	tr := NewTracker(time.Minute, nil)
	tr.Register("a", "", os.Getpid())
	tr.Close()
	// Close should stop tickers/timers without panicking; nothing to assert
	// beyond not deadlocking/panicking.
}
