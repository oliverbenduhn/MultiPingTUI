package main

import "sync"

// HostRepository defines the interface for accessing and modifying host wrappers
type HostRepository interface {
	GetAll() []PingWrapperInterface
	UpdateAll(wrappers []PingWrapperInterface)
}

// MemoryHostRepository is an in-memory implementation of HostRepository
type MemoryHostRepository struct {
	wrappers []PingWrapperInterface
	mu       sync.RWMutex
}

// NewMemoryHostRepository creates a new MemoryHostRepository
func NewMemoryHostRepository() *MemoryHostRepository {
	return &MemoryHostRepository{
		wrappers: make([]PingWrapperInterface, 0),
	}
}

// GetAll returns a copy of the current wrappers
func (r *MemoryHostRepository) GetAll() []PingWrapperInterface {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]PingWrapperInterface, len(r.wrappers))
	copy(out, r.wrappers)
	return out
}

// UpdateAll replaces the current wrappers with the new list
func (r *MemoryHostRepository) UpdateAll(wrappers []PingWrapperInterface) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrappers = wrappers
}
