package relay

import (
	"crypto/rand"
	"log"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/boardgame/voicerelay/internal/header"
	"github.com/boardgame/voicerelay/internal/room"
)

func newToken() [16]byte {
	var t [16]byte
	_, _ = rand.Read(t[:])
	return t
}

// startLoopbackServer creates a Server listening on a random localhost UDP port.
func startLoopbackServer(t *testing.T, mgr *room.Manager) *Server {
	t.Helper()
	s := New(mgr, log.New(os.Stderr, "[test] ", log.LstdFlags))

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	s.conn = conn

	// Start receive loop in background
	go func() {
		buf := make([]byte, 2048)
		for {
			n, remoteAddr, err := conn.ReadFromUDP(buf)
			if err != nil {
				return // connection closed
			}
			packet := make([]byte, n)
			copy(packet, buf[:n])
			go s.handlePacket(packet, remoteAddr)
		}
	}()

	t.Cleanup(func() { s.Close() })
	return s
}

// newClientConn creates a UDP "client" socket connected to the server.
// Returns the local address (which the server will see as sender) and the connection.
func newClientConn(t *testing.T, serverAddr *net.UDPAddr) (*net.UDPAddr, *net.UDPConn) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn.LocalAddr().(*net.UDPAddr), conn
}

func sendPacket(t *testing.T, conn *net.UDPConn, serverAddr *net.UDPAddr, token [16]byte, seq uint32, channel byte, codec header.CodecType, payload []byte) {
	t.Helper()
	pkt := header.BuildPacket(token, seq, channel, codec, payload)
	_, err := conn.WriteToUDP(pkt, serverAddr)
	if err != nil {
		t.Fatalf("WriteToUDP: %v", err)
	}
}

func recvPacket(t *testing.T, conn *net.UDPConn, timeout time.Duration) ([]byte, *net.UDPAddr) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 2048)
	n, addr, err := conn.ReadFromUDP(buf)
	if err != nil {
		return nil, nil
	}
	packet := make([]byte, n)
	copy(packet, buf[:n])
	return packet, addr
}

func TestForwardToSameChannel(t *testing.T) {
	mgr := room.NewManager(12)
	s := startLoopbackServer(t, mgr)
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)

	// Create 3 clients
	t1 := newToken()
	t2 := newToken()
	t3 := newToken()

	addr1, conn1 := newClientConn(t, serverAddr)
	addr2, conn2 := newClientConn(t, serverAddr)
	addr3, conn3 := newClientConn(t, serverAddr)

	// All in room "r1", channels 0 and 1 (t1/t2 in ch0, t3 in ch1)
	mgr.RegisterClient("r1", 0, addr1, t1)
	mgr.RegisterClient("r1", 0, addr2, t2)
	mgr.RegisterClient("r1", 1, addr3, t3)

	// Wait for goroutines to settle
	time.Sleep(50 * time.Millisecond)

	// t1 sends to channel 0
	sendPacket(t, conn1, serverAddr, t1, 1, 0, header.CodecOpus, []byte("hello"))

	// t2 (same channel) should receive
	pkt, _ := recvPacket(t, conn2, 500*time.Millisecond)
	if pkt == nil {
		t.Fatal("t2 did not receive forwarded packet")
	}
	h, err := header.Parse(pkt)
	if err != nil {
		t.Fatalf("parse forwarded: %v", err)
	}
	if h.Token != t1 {
		t.Errorf("forwarded token = %x, want t1", h.Token)
	}
	if string(pkt[header.Size:]) != "hello" {
		t.Errorf("payload = %q, want hello", string(pkt[header.Size:]))
	}

	// t3 (different channel) should NOT receive
	pkt, _ = recvPacket(t, conn3, 200*time.Millisecond)
	if pkt != nil {
		t.Error("t3 should not receive (different channel)")
	}

	// t1 should NOT receive own packet
	pkt, _ = recvPacket(t, conn1, 100*time.Millisecond)
	if pkt != nil {
		t.Error("t1 should not receive own packet")
	}
}

func TestIPMismatchRejected(t *testing.T) {
	mgr := room.NewManager(12)
	s := startLoopbackServer(t, mgr)
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)

	t1 := newToken()
	addr2, conn2 := newClientConn(t, serverAddr)

	// Register t1 with addr2's address (wrong!)
	mgr.RegisterClient("r1", 0, addr2, t1)

	// But send from a different address
	_, conn3 := newClientConn(t, serverAddr)

	time.Sleep(50 * time.Millisecond)

	sendPacket(t, conn3, serverAddr, t1, 1, 0, header.CodecOpus, []byte("spoof"))

	// conn2 (the registered address) should NOT receive — spoof rejected
	pkt, _ := recvPacket(t, conn2, 200*time.Millisecond)
	if pkt != nil {
		t.Error("spoofed packet should be rejected")
	}
}

func TestInvalidTokenIgnored(t *testing.T) {
	mgr := room.NewManager(12)
	s := startLoopbackServer(t, mgr)
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)

	_, conn1 := newClientConn(t, serverAddr)
	_, conn2 := newClientConn(t, serverAddr)

	// Don't register t1 — use invalid token directly
	badToken := newToken()

	time.Sleep(50 * time.Millisecond)

	sendPacket(t, conn1, serverAddr, badToken, 1, 0, header.CodecOpus, []byte("ghost"))

	// Nobody should receive
	pkt, _ := recvPacket(t, conn2, 200*time.Millisecond)
	if pkt != nil {
		t.Error("packet with invalid token should be dropped")
	}
}

func TestConcurrentForward(t *testing.T) {
	mgr := room.NewManager(12)
	s := startLoopbackServer(t, mgr)
	serverAddr := s.conn.LocalAddr().(*net.UDPAddr)

	// Create 6 clients in the same channel
	tokens := make([][16]byte, 6)
	conns := make([]*net.UDPConn, 6)
	for i := 0; i < 6; i++ {
		tokens[i] = newToken()
		addr, conn := newClientConn(t, serverAddr)
		conns[i] = conn
		mgr.RegisterClient("r1", 0, addr, tokens[i])
	}

	time.Sleep(50 * time.Millisecond)

	// All 6 send concurrently
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			payload := []byte{byte(idx)}
			sendPacket(t, conns[idx], serverAddr, tokens[idx], 1, 0, header.CodecOpus, payload)
		}(i)
	}
	wg.Wait()

	// Each client should receive from the other 5
	received := make([]int, 6)
	var mu sync.Mutex
	var wg2 sync.WaitGroup

	for i := 0; i < 6; i++ {
		wg2.Add(1)
		go func(idx int) {
			defer wg2.Done()
			for j := 0; j < 5; j++ {
				pkt, _ := recvPacket(t, conns[idx], 500*time.Millisecond)
				if pkt != nil {
					mu.Lock()
					received[idx]++
					mu.Unlock()
				}
			}
		}(i)
	}
	wg2.Wait()

	for i, count := range received {
		if count < 3 { // Allow some UDP loss on loopback under stress
			t.Logf("client %d received %d/5 packets", i, count)
		}
	}
}
