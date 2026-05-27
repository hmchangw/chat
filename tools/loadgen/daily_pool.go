package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// directPool owns one nats.Conn per simulated user plus one subscription per
// user-room pair. Each subscription callback records broadcast-arrival time
// against the shared Collector for latency correlation.
//
// The pool is wired into the daily-IM runtime by a later task; suppress the
// "unused" linter until that wiring lands so this commit can stand alone.
//
//nolint:unused // wired up in the daily-IM runtime task that follows
type directPool struct {
	url       string
	collector *Collector

	mu    sync.Mutex
	users map[string]*directUser
}

//nolint:unused // wired up in the daily-IM runtime task that follows
type directUser struct {
	id   string
	nc   *nats.Conn
	subs []*nats.Subscription
}

//nolint:unused // wired up in the daily-IM runtime task that follows
func newDirectPool(natsURL string, c *Collector) *directPool {
	return &directPool{
		url: natsURL, collector: c, users: make(map[string]*directUser),
	}
}

// Add opens a connection for u and subscribes to every room in u.Rooms.
// Safe to call concurrently for different users.
//
//nolint:unused // wired up in the daily-IM runtime task that follows
func (p *directPool) Add(u *userState) error {
	nc, err := nats.Connect(p.url, nats.Name("loadgen-daily-"+u.ID))
	if err != nil {
		return fmt.Errorf("connect for %s: %w", u.ID, err)
	}
	du := &directUser{id: u.ID, nc: nc}
	for _, roomID := range u.Rooms {
		sub, err := nc.Subscribe(subject.RoomEvent(roomID), func(m *nats.Msg) {
			p.onBroadcast(m)
		})
		if err != nil {
			_ = nc.Drain()
			return fmt.Errorf("subscribe %s/%s: %w", u.ID, roomID, err)
		}
		du.subs = append(du.subs, sub)
	}
	p.mu.Lock()
	p.users[u.ID] = du
	p.mu.Unlock()
	return nil
}

// Size reports the number of users currently in the pool.
//
//nolint:unused // wired up in the daily-IM runtime task that follows
func (p *directPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.users)
}

//nolint:unused // wired up in the daily-IM runtime task that follows
func (p *directPool) onBroadcast(m *nats.Msg) {
	var evt model.RoomEvent
	if err := json.Unmarshal(m.Data, &evt); err != nil {
		return // ignore malformed
	}
	if evt.LastMsgID == "" {
		return
	}
	p.collector.RecordBroadcast(evt.LastMsgID, time.Now())
}

// Close drains all connections.
//
//nolint:unused // wired up in the daily-IM runtime task that follows
func (p *directPool) Close() {
	p.mu.Lock()
	users := p.users
	p.users = nil
	p.mu.Unlock()
	for _, du := range users {
		_ = du.nc.Drain()
	}
}
