package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/model"
)

// Store defines the interface for chat data persistence.
type Store interface {
	CreateRoom(name, createdBy string) (model.Room, error)
	ListRooms() ([]model.Room, error)
	GetRoom(id string) (model.Room, error)
	SendMessage(roomID, userID, content string) (model.Message, error)
	ListMessages(roomID string, limit int) ([]model.Message, error)
}

// Memory is an in-memory implementation of Store.
type Memory struct {
	mu       sync.RWMutex
	rooms    map[string]model.Room
	messages map[string][]model.Message // roomID -> messages
	nextID   int
}

func NewMemory() *Memory {
	return &Memory{
		rooms:    make(map[string]model.Room),
		messages: make(map[string][]model.Message),
	}
}

func (m *Memory) genID() string {
	m.nextID++
	return fmt.Sprintf("%d", m.nextID)
}

func (m *Memory) CreateRoom(name, createdBy string) (model.Room, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room := model.Room{
		ID:        m.genID(),
		Name:      name,
		CreatedBy: createdBy,
		CreatedAt: time.Now().UTC(),
	}
	m.rooms[room.ID] = room
	return room, nil
}

func (m *Memory) ListRooms() ([]model.Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rooms := make([]model.Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	return rooms, nil
}

func (m *Memory) GetRoom(id string) (model.Room, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, ok := m.rooms[id]
	if !ok {
		return model.Room{}, fmt.Errorf("room %q not found", id)
	}
	return room, nil
}

func (m *Memory) SendMessage(roomID, userID, content string) (model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.rooms[roomID]; !ok {
		return model.Message{}, fmt.Errorf("room %q not found", roomID)
	}

	msg := model.Message{
		ID:        m.genID(),
		RoomID:    roomID,
		UserID:    userID,
		Content:   content,
		CreatedAt: time.Now().UTC(),
	}
	m.messages[roomID] = append(m.messages[roomID], msg)
	return msg, nil
}

func (m *Memory) ListMessages(roomID string, limit int) ([]model.Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	msgs := m.messages[roomID]
	if limit > 0 && limit < len(msgs) {
		msgs = msgs[len(msgs)-limit:]
	}
	result := make([]model.Message, len(msgs))
	copy(result, msgs)
	return result, nil
}
