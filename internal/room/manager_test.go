package room

import (
	"crypto/rand"
	"net"
	"testing"
	"time"
)

func newToken() [16]byte {
	var t [16]byte
	_, _ = rand.Read(t[:])
	return t
}

func newAddr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

func TestRegisterAndGet(t *testing.T) {
	m := NewManager(12)
	token := newToken()
	addr := newAddr(9000)

	client, ok := m.RegisterClient("room1", 1, addr, token)
	if !ok {
		t.Fatal("RegisterClient failed")
	}
	if client.Token != token {
		t.Error("token mismatch")
	}
	if client.Channel != 1 {
		t.Errorf("channel = %d, want 1", client.Channel)
	}

	got := m.GetClient(token)
	if got == nil {
		t.Fatal("GetClient returned nil")
	}
	if got.Addr.Port != 9000 {
		t.Errorf("port = %d, want 9000", got.Addr.Port)
	}
}

func TestUnregister(t *testing.T) {
	m := NewManager(12)
	token := newToken()
	m.RegisterClient("room1", 1, newAddr(1), token)

	if !m.UnregisterClient(token) {
		t.Error("UnregisterClient should succeed")
	}
	if m.GetClient(token) != nil {
		t.Error("client should be gone after unregister")
	}
	if m.UnregisterClient(token) {
		t.Error("second UnregisterClient should return false")
	}
}

func TestChannelMembers(t *testing.T) {
	m := NewManager(12)
	t1 := newToken()
	t2 := newToken()
	t3 := newToken()

	m.RegisterClient("r1", 0, newAddr(1), t1)
	m.RegisterClient("r1", 0, newAddr(2), t2)
	m.RegisterClient("r1", 1, newAddr(3), t3) // different channel

	members := m.ChannelMembers("r1", 0)
	if len(members) != 2 {
		t.Fatalf("channel 0 members = %d, want 2", len(members))
	}

	members = m.ChannelMembers("r1", 1)
	if len(members) != 1 {
		t.Fatalf("channel 1 members = %d, want 1", len(members))
	}

	// Test non-existent room
	if len(m.ChannelMembers("no-such-room", 0)) != 0 {
		t.Error("non-existent room should return empty slice")
	}
}

func TestUpdateChannel(t *testing.T) {
	m := NewManager(12)
	token := newToken()
	m.RegisterClient("r1", 0, newAddr(1), token)

	if !m.UpdateChannel(token, 2) {
		t.Error("UpdateChannel failed")
	}

	client := m.GetClient(token)
	if client.Channel != 2 {
		t.Errorf("channel = %d, want 2", client.Channel)
	}

	// Old channel should be empty
	if len(m.ChannelMembers("r1", 0)) != 0 {
		t.Error("old channel should be empty")
	}
	// New channel should have the client
	if len(m.ChannelMembers("r1", 2)) != 1 {
		t.Error("new channel should have the client")
	}

	// Same channel — no-op
	if !m.UpdateChannel(token, 2) {
		t.Error("UpdateChannel to same channel should succeed (no-op)")
	}

	// Non-existent token
	if m.UpdateChannel(newToken(), 0) {
		t.Error("UpdateChannel with unknown token should fail")
	}
}

func TestMaxPerRoom(t *testing.T) {
	m := NewManager(2)
	t1 := newToken()
	t2 := newToken()
	t3 := newToken()

	m.RegisterClient("r1", 0, newAddr(1), t1)
	m.RegisterClient("r1", 0, newAddr(2), t2)

	_, ok := m.RegisterClient("r1", 0, newAddr(3), t3)
	if ok {
		t.Error("third client should be rejected (room full)")
	}

	// But a different room should work
	_, ok = m.RegisterClient("r2", 0, newAddr(3), t3)
	if !ok {
		t.Error("different room should accept")
	}
}

func TestPurgeExpired(t *testing.T) {
	m := NewManager(12)
	t1 := newToken()
	t2 := newToken()

	m.RegisterClient("r1", 0, newAddr(1), t1)
	m.RegisterClient("r1", 0, newAddr(2), t2)

	// Simulate t2 being idle for 5 seconds by setting its last packet time back
	c2 := m.GetClient(t2)
	c2.SetLastPacket(time.Now().Add(-5 * time.Second))

	removed := m.PurgeExpired(2 * time.Second)
	if removed != 1 {
		t.Errorf("PurgeExpired removed %d, want 1", removed)
	}

	// t2 should be gone
	if m.GetClient(t2) != nil {
		t.Error("t2 should have been purged")
	}
	// t1 should still be there
	if m.GetClient(t1) == nil {
		t.Error("t1 should still be present")
	}
	// Room should still exist
	if m.RoomCount("r1") != 1 {
		t.Errorf("room count = %d, want 1", m.RoomCount("r1"))
	}

	// Now expire t1 as well — room should be cleaned up
	c1 := m.GetClient(t1)
	c1.SetLastPacket(time.Now().Add(-10 * time.Second))
	removed = m.PurgeExpired(1 * time.Second)
	if removed != 1 {
		t.Errorf("second PurgeExpired removed %d, want 1", removed)
	}
	if m.RoomCount("r1") != 0 {
		t.Errorf("room should be empty after all clients purged, got %d", m.RoomCount("r1"))
	}
}

func TestStats(t *testing.T) {
	m := NewManager(12)
	t1 := newToken()
	t2 := newToken()
	t3 := newToken()

	m.RegisterClient("r1", 0, newAddr(1), t1)
	m.RegisterClient("r1", 1, newAddr(2), t2)
	m.RegisterClient("r2", 0, newAddr(3), t3)

	rooms, clients := m.Stats()
	if rooms != 2 {
		t.Errorf("rooms = %d, want 2", rooms)
	}
	if clients != 3 {
		t.Errorf("clients = %d, want 3", clients)
	}
}

func TestConcurrentAccess(t *testing.T) {
	m := NewManager(100)
	done := make(chan bool)

	// Concurrent registrations
	for i := 0; i < 10; i++ {
		go func(id int) {
			token := newToken()
			addr := newAddr(9000 + id)
			m.RegisterClient("r1", byte(id%5), addr, token)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	rooms, clients := m.Stats()
	if rooms != 1 {
		t.Errorf("rooms = %d, want 1", rooms)
	}
	if clients != 10 {
		t.Errorf("clients = %d, want 10", clients)
	}
}
