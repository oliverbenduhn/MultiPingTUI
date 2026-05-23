package main

import (
	"testing"
	"time"
)

// MockWrapper for PingService testing
type MockWrapper struct {
	host      string
	started   bool
	stopped   bool
	startChan chan bool // optional channel to signal start
	release   chan struct{}
	starts    int
	stops     int
}

func (m *MockWrapper) Start() {
	m.started = true
	m.starts++
	if m.startChan != nil {
		m.startChan <- true
	}
	if m.release != nil {
		<-m.release
	}
}

func (m *MockWrapper) Stop() {
	m.stopped = true
	m.stops++
}

func (m *MockWrapper) Host() string            { return m.host }
func (m *MockWrapper) CalcStats(int64) PWStats { return PWStats{} }
func (m *MockWrapper) Stats() *PWStats         { return &PWStats{} }
func (m *MockWrapper) SetHostRepr(string)      {}

func TestPingService_InitHosts(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	// Override factory
	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		return &MockWrapper{host: host}
	}

	hosts := []string{"host1", "host2"}
	ps.InitHosts(hosts)

	stored := repo.GetAll()
	if len(stored) != 2 {
		t.Errorf("Expected 2 hosts, got %d", len(stored))
	}
	if stored[0].Host() != "host1" {
		t.Errorf("Expected host1, got %s", stored[0].Host())
	}
}

func TestPingService_StartStop(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	// Override factory
	wrappers := make([]*MockWrapper, 0)
	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		w := &MockWrapper{host: host}
		wrappers = append(wrappers, w)
		return w
	}

	ps.InitHosts([]string{"host1"})

	// Test Start
	ps.Start()

	if !wrappers[0].started {
		t.Error("Start() did not start the wrapper")
	}

	// Test Stop
	ps.Stop()

	if !wrappers[0].stopped {
		t.Error("Stop() did not stop the wrapper")
	}
}

func TestPingService_StartStopAreIdempotent(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	var wrapper *MockWrapper
	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		wrapper = &MockWrapper{host: host}
		return wrapper
	}

	ps.InitHosts([]string{"host1"})
	ps.Start()
	ps.Start()
	ps.Stop()
	ps.Stop()

	if wrapper.starts != 1 {
		t.Fatalf("expected one start, got %d", wrapper.starts)
	}
	if wrapper.stops != 1 {
		t.Fatalf("expected one stop, got %d", wrapper.stops)
	}
}

func TestPingService_ReplaceHosts(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	// Override factory
	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		return &MockWrapper{host: host}
	}

	// Initial hosts
	ps.InitHosts([]string{"old1"})
	ps.Start()
	oldWrappers := repo.GetAll()
	oldMock := oldWrappers[0].(*MockWrapper)

	// Replace hosts
	ps.ReplaceHosts([]string{"new1", "new2"})

	// Verify repo updated
	newWrappers := repo.GetAll()
	if len(newWrappers) != 2 {
		t.Errorf("Expected 2 new hosts, got %d", len(newWrappers))
	}
	if newWrappers[0].Host() != "new1" {
		t.Errorf("Expected new1, got %s", newWrappers[0].Host())
	}

	// Verify old wrapper stopped
	if !oldMock.stopped {
		t.Error("Old wrapper was not stopped")
	}

	// Verify new wrappers started
	if !newWrappers[0].(*MockWrapper).started {
		t.Error("New wrapper was not started")
	}
}

func TestPingService_ReplaceHostsWhileStoppedDoesNotStart(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		return &MockWrapper{host: host}
	}

	ps.InitHosts([]string{"old1"})
	oldWrapper := repo.GetAll()[0].(*MockWrapper)
	ps.ReplaceHosts([]string{"new1"})
	newWrapper := repo.GetAll()[0].(*MockWrapper)

	if oldWrapper.stopped {
		t.Fatal("old stopped wrapper should not be stopped when service was not running")
	}
	if newWrapper.started {
		t.Fatal("new wrapper should not start when service was not running")
	}
}

func TestPingService_ReplaceHostsStartsNewWrappersInParallel(t *testing.T) {
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, Options{}, nil)

	started := make(chan bool, 2)
	release := make(chan struct{})
	ps.wrapperFactory = func(host string, options Options, tw *TransitionWriter) PingWrapperInterface {
		w := &MockWrapper{host: host}
		if host == "new1" || host == "new2" {
			w.startChan = started
			w.release = release
		}
		return w
	}

	ps.InitHosts([]string{"old1"})
	ps.Start()

	done := make(chan struct{})
	go func() {
		ps.ReplaceHosts([]string{"new1", "new2"})
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for parallel starts")
		}
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("replace did not finish after releasing starts")
	}
	ps.Stop()
}
