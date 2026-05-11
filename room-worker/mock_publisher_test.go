package main

import "sync"

// mockPublisher captures NATS publishes for use in unit tests.
type mockPublisher struct {
	mu       sync.Mutex
	subjects []string
	payloads [][]byte
}

func (p *mockPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	p.payloads = append(p.payloads, append([]byte(nil), data...))
	return nil
}

func (p *mockPublisher) publishCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subjects)
}
