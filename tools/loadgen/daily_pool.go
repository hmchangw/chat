package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/subject"
)

// directPool owns one nats.Conn per simulated user plus one subscription per
// user-room pair. Each subscription callback records broadcast-arrival time
// against the shared Collector for latency correlation.
type directPool struct {
	url       string
	collector *Collector

	mu    sync.Mutex
	users map[string]*directUser
}

type directUser struct {
	id   string
	nc   *nats.Conn
	subs []*nats.Subscription
}

func newDirectPool(natsURL string, c *Collector) *directPool {
	return &directPool{
		url: natsURL, collector: c, users: make(map[string]*directUser),
	}
}

// Add opens a connection for u and subscribes to every room in u.Rooms.
// Safe to call concurrently for different users.
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
func (p *directPool) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.users)
}

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
func (p *directPool) Close() {
	p.mu.Lock()
	users := p.users
	p.users = nil
	p.mu.Unlock()
	for _, du := range users {
		_ = du.nc.Drain()
	}
}

// multiplexPool fans M shared NATS connections across N users. Each shared
// connection subscribes (with reference counting) to the union of room
// broadcast subjects for its assigned users. Incoming messages are routed
// to per-user inbox channels via the dispatch map.
type multiplexPool struct {
	url       string
	collector *Collector
	conns     []*nats.Conn

	mu        sync.Mutex
	roomRefs  map[string]int              // roomID -> ref count on the shared conns
	dispatch  map[string][]chan *nats.Msg // roomID -> per-user inboxes
	userInbox map[string]chan *nats.Msg   // userID -> that user's inbox channel
	nextConn  int                         // round-robin assignment
}

func newMultiplexPool(natsURL string, c *Collector, size int) *multiplexPool {
	p := &multiplexPool{
		url: natsURL, collector: c,
		roomRefs:  make(map[string]int),
		dispatch:  make(map[string][]chan *nats.Msg),
		userInbox: make(map[string]chan *nats.Msg),
	}
	for i := 0; i < size; i++ {
		nc, err := nats.Connect(natsURL, nats.Name(fmt.Sprintf("loadgen-daily-mux-%d", i)))
		if err != nil {
			p.Close()
			panic(fmt.Errorf("multiplex conn %d: %w", i, err))
		}
		p.conns = append(p.conns, nc)
	}
	return p
}

// Add registers a user with the multiplex pool.
func (p *multiplexPool) Add(u *userState) error {
	inbox := make(chan *nats.Msg, 128)
	p.mu.Lock()
	p.userInbox[u.ID] = inbox
	for _, roomID := range u.Rooms {
		p.dispatch[roomID] = append(p.dispatch[roomID], inbox)
		if p.roomRefs[roomID] == 0 && len(p.conns) > 0 {
			nc := p.conns[p.nextConn%len(p.conns)]
			p.nextConn++
			subj := subject.RoomEvent(roomID)
			if _, err := nc.Subscribe(subj, p.route); err != nil {
				p.mu.Unlock()
				return fmt.Errorf("multiplex subscribe %s: %w", roomID, err)
			}
		}
		p.roomRefs[roomID]++
	}
	p.mu.Unlock()
	return nil
}

// route is called by every shared conn's subscription callback. It looks up
// the destination inboxes by RoomID and does a non-blocking send.
func (p *multiplexPool) route(m *nats.Msg) {
	var evt model.RoomEvent
	if err := json.Unmarshal(m.Data, &evt); err != nil {
		return
	}
	roomID := evt.RoomID
	if roomID == "" {
		roomID = parseRoomFromSubject(m.Subject)
	}
	p.mu.Lock()
	inboxes := p.dispatch[roomID]
	p.mu.Unlock()
	if evt.LastMsgID != "" {
		p.collector.RecordBroadcast(evt.LastMsgID, time.Now())
	}
	for _, ch := range inboxes {
		select {
		case ch <- m:
		default:
			p.collector.RecordMultiplexDrop()
		}
	}
}

// parseRoomFromSubject extracts the room ID from a "chat.room.<id>.event" subject.
func parseRoomFromSubject(subj string) string {
	parts := strings.Split(subj, ".")
	if len(parts) >= 3 && parts[0] == "chat" && parts[1] == "room" {
		return parts[2]
	}
	return ""
}

// Close drains shared conns and closes inboxes.
func (p *multiplexPool) Close() {
	p.mu.Lock()
	inboxes := p.userInbox
	p.userInbox = nil
	p.dispatch = nil
	p.roomRefs = nil
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, nc := range conns {
		_ = nc.Drain()
	}
	for _, ch := range inboxes {
		close(ch)
	}
}
