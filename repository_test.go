package main

import (
	"testing"
)

// MockPingWrapper for repository testing
type MockPingWrapper struct {
	host string
}

func (m *MockPingWrapper) Start()                   {}
func (m *MockPingWrapper) Stop()                    {}
func (m *MockPingWrapper) Host() string             { return m.host }
func (m *MockPingWrapper) CalcStats(int64) PWStats  { return PWStats{} }
func (m *MockPingWrapper) Stats() *PWStats          { return &PWStats{} }
func (m *MockPingWrapper) SetHostRepr(string)       {}

func TestNewMemoryHostRepository(t *testing.T) {
	repo := NewMemoryHostRepository()
	if repo == nil {
		t.Fatal("NewMemoryHostRepository returned nil")
	}
	if len(repo.GetAll()) != 0 {
		t.Errorf("Expected empty repository, got %d items", len(repo.GetAll()))
	}
}

func TestUpdateAllAndGetAll(t *testing.T) {
	repo := NewMemoryHostRepository()

	wrappers := []PingWrapperInterface{
		&MockPingWrapper{host: "host1"},
		&MockPingWrapper{host: "host2"},
	}

	repo.UpdateAll(wrappers)

	stored := repo.GetAll()
	if len(stored) != 2 {
		t.Errorf("Expected 2 items, got %d", len(stored))
	}

	if stored[0].Host() != "host1" {
		t.Errorf("Expected first host to be 'host1', got '%s'", stored[0].Host())
	}
}

func TestGetAllCopy(t *testing.T) {
	repo := NewMemoryHostRepository()
	wrappers := []PingWrapperInterface{
		&MockPingWrapper{host: "host1"},
	}
	repo.UpdateAll(wrappers)

	// Modify the returned slice
	got := repo.GetAll()
	got[0] = &MockPingWrapper{host: "modified"}

	// Verify internal state is unchanged
	stored := repo.GetAll()
	if stored[0].Host() != "host1" {
		t.Error("GetAll should return a copy, but modifying it affected internal state")
	}
}

func TestUpdateWithEmptyList(t *testing.T) {
	repo := NewMemoryHostRepository()
	wrappers := []PingWrapperInterface{
		&MockPingWrapper{host: "host1"},
	}
	repo.UpdateAll(wrappers)

	repo.UpdateAll([]PingWrapperInterface{})

	if len(repo.GetAll()) != 0 {
		t.Error("Expected repository to be empty after update with empty list")
	}
}
