// Package room manages voice rooms, channels, and connected clients.
// All methods are safe for concurrent use.
package room

import (
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Client represents a connected voice client.
type Client struct {
	Token      [16]byte
	Addr       *net.UDPAddr
	RoomID     string
	Channel    byte
	lastPacket int64 // unix nano, accessed atomically
}

// LastPacket returns the time of the last received voice packet.
func (c *Client) LastPacket() time.Time {
	return time.Unix(0, atomic.LoadInt64(&c.lastPacket))
}

// Touch updates the last-packet timestamp to now.
func (c *Client) Touch() {
	atomic.StoreInt64(&c.lastPacket, time.Now().UnixNano())
}

// SetLastPacket sets the last-packet timestamp explicitly (for testing).
func (c *Client) SetLastPacket(t time.Time) {
	atomic.StoreInt64(&c.lastPacket, t.UnixNano())
}

// Room holds all channels for a single game room.
type Room struct {
	ID       string
	channels map[byte][]*Client // channel → clients
}

// Manager owns all rooms and provides thread-safe access.
type Manager struct {
	mu         sync.RWMutex
	rooms      map[string]*Room
	clientIdx  map[[16]byte]*Client // token → client (for fast lookup)
	maxPerRoom int
}

// NewManager creates a Manager with the given per-room client limit.
func NewManager(maxPerRoom int) *Manager {
	return &Manager{
		rooms:      make(map[string]*Room),
		clientIdx:  make(map[[16]byte]*Client),
		maxPerRoom: maxPerRoom,
	}
}

// RegisterClient adds a client to a room/channel. If the room doesn't exist it is created.
// Returns the Client and true on success, or nil and false if the room is full.
func (m *Manager) RegisterClient(roomID string, channel byte, addr *net.UDPAddr, token [16]byte) (*Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	room, ok := m.rooms[roomID]
	if !ok {
		room = &Room{ID: roomID, channels: make(map[byte][]*Client)}
		m.rooms[roomID] = room
	}

	// Count existing clients in this room
	total := 0
	for _, clients := range room.channels {
		total += len(clients)
	}
	if total >= m.maxPerRoom {
		return nil, false
	}

	client := &Client{
		Token:   token,
		Addr:    addr,
		RoomID:  roomID,
		Channel: channel,
	}
	client.Touch()

	room.channels[channel] = append(room.channels[channel], client)
	m.clientIdx[token] = client
	return client, true
}

// UnregisterClient removes a client by token. Returns true if found and removed.
func (m *Manager) UnregisterClient(token [16]byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clientIdx[token]
	if !ok {
		return false
	}
	delete(m.clientIdx, token)

	room, ok := m.rooms[client.RoomID]
	if !ok {
		return true
	}

	clients := room.channels[client.Channel]
	for i, c := range clients {
		if c.Token == token {
			room.channels[client.Channel] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	if len(room.channels[client.Channel]) == 0 {
		delete(room.channels, client.Channel)
	}
	if len(room.channels) == 0 {
		delete(m.rooms, client.RoomID)
	}
	return true
}

// GetClient looks up a client by token.
func (m *Manager) GetClient(token [16]byte) *Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.clientIdx[token]
}

// ChannelMembers returns all clients in the given room and channel.
func (m *Manager) ChannelMembers(roomID string, channel byte) []*Client {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, ok := m.rooms[roomID]
	if !ok {
		return nil
	}
	// Return a copy to avoid races with the caller holding a snapshot
	orig := room.channels[channel]
	out := make([]*Client, len(orig))
	copy(out, orig)
	return out
}

// UpdateChannel moves a client to a new channel within the same room.
func (m *Manager) UpdateChannel(token [16]byte, newChannel byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clientIdx[token]
	if !ok {
		return false
	}
	if client.Channel == newChannel {
		return true
	}

	room := m.rooms[client.RoomID]

	// Remove from old channel
	oldClients := room.channels[client.Channel]
	for i, c := range oldClients {
		if c.Token == token {
			room.channels[client.Channel] = append(oldClients[:i], oldClients[i+1:]...)
			break
		}
	}
	if len(room.channels[client.Channel]) == 0 {
		delete(room.channels, client.Channel)
	}

	// Add to new channel
	client.Channel = newChannel
	room.channels[newChannel] = append(room.channels[newChannel], client)
	return true
}

// PurgeExpired removes clients that haven't sent a packet within timeout.
// Returns the number of clients removed.
func (m *Manager) PurgeExpired(timeout time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now().UnixNano()
	deadline := now - int64(timeout)
	count := 0

	for token, client := range m.clientIdx {
		if atomic.LoadInt64(&client.lastPacket) < deadline {
			m.removeClientLocked(token)
			count++
		}
	}
	return count
}

// removeClientLocked removes a client. Must be called with m.mu held (write lock).
func (m *Manager) removeClientLocked(token [16]byte) {
	client, ok := m.clientIdx[token]
	if !ok {
		return
	}
	delete(m.clientIdx, token)

	room, ok := m.rooms[client.RoomID]
	if !ok {
		return
	}

	clients := room.channels[client.Channel]
	for i, c := range clients {
		if c.Token == token {
			room.channels[client.Channel] = append(clients[:i], clients[i+1:]...)
			break
		}
	}
	if len(room.channels[client.Channel]) == 0 {
		delete(room.channels, client.Channel)
	}
	if len(room.channels) == 0 {
		delete(m.rooms, client.RoomID)
	}
}

// Stats returns current room and client counts.
func (m *Manager) Stats() (rooms int, clients int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.rooms), len(m.clientIdx)
}

// RoomCount returns the number of clients in a specific room.
func (m *Manager) RoomCount(roomID string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	room, ok := m.rooms[roomID]
	if !ok {
		return 0
	}
	total := 0
	for _, clients := range room.channels {
		total += len(clients)
	}
	return total
}

