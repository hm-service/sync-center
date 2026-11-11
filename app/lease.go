package app

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

type Lease struct {
	ID        string
	Namespace string
	ExpiresAt time.Time
}

type LeaseStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[string]*Lease
}

func NewLeaseStore(ttl time.Duration) *LeaseStore {
	return &LeaseStore{ttl: ttl, items: make(map[string]*Lease)}
}

func (s *LeaseStore) purgeLocked(now time.Time) {
	for id, lease := range s.items {
		if !lease.ExpiresAt.After(now) {
			delete(s.items, id)
		}
	}
}

// Acquire 每个 namespace 同时只允许一个未过期租约。
func (s *LeaseStore) Acquire(namespace string) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	if _, ok := s.items[namespace]; ok {
		return nil, false
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return nil, false
	}
	lease := &Lease{
		ID:        hex.EncodeToString(raw),
		Namespace: namespace,
		ExpiresAt: now.Add(s.ttl),
	}
	s.items[namespace] = lease
	return lease, true
}

func (s *LeaseStore) Peek(id, namespace string) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	lease, ok := s.items[namespace]
	if !ok || lease.ID != id {
		return nil, false
	}
	return lease, true
}

func (s *LeaseStore) Heartbeat(id, namespace string) (*Lease, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	lease, ok := s.items[namespace]
	if !ok || lease.ID != id {
		return nil, false
	}
	lease.ExpiresAt = now.Add(s.ttl)
	return lease, true
}

func (s *LeaseStore) Release(id, namespace string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.items[namespace]
	if !ok || lease.ID != id {
		return false
	}
	delete(s.items, namespace)
	return true
}
