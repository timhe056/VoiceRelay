// Package relay implements the core voice packet forwarding engine.
// It receives UDP packets, validates the header, and forwards them
// to all other clients in the same voice channel.
package relay

import (
	"log"
	"net"

	"github.com/boardgame/voicerelay/internal/header"
	"github.com/boardgame/voicerelay/internal/room"
)

// Server holds the UDP listener and routes voice packets.
type Server struct {
	conn   *net.UDPConn
	mgr    *room.Manager
	logger *log.Logger
}

// New creates a Server with the given room manager.
func New(mgr *room.Manager, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	return &Server{
		mgr:    mgr,
		logger: logger,
	}
}

// ListenUDP binds to addr (e.g. ":9000") and starts the receive loop.
// This method blocks until the connection is closed.
func (s *Server) ListenUDP(addr string) error {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return err
	}
	s.conn = conn

	buf := make([]byte, 2048)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			s.logger.Printf("UDP read error: %v", err)
			continue
		}
		// Copy data since buf will be reused on next read
		packet := make([]byte, n)
		copy(packet, buf[:n])

		go s.handlePacket(packet, remoteAddr)
	}
}

// handlePacket processes a single incoming voice packet on its own goroutine.
func (s *Server) handlePacket(data []byte, sender *net.UDPAddr) {
	if len(data) < header.Size {
		return
	}

	hdr, err := header.Parse(data[:header.Size])
	if err != nil {
		return
	}
	payloadLen := len(data) - header.Size

	// Token lookup
	client := s.mgr.GetClient(hdr.Token)
	if client == nil {
		// Invalid or expired token — log once per burst?
		return
	}

	// Verify sender IP (weak guard — token auth in header is the real security).
	if !ipsMatch(client.Addr.IP, sender.IP) {
		s.logger.Printf("IP mismatch for token %x: registered %v, got %v",
			hdr.Token, client.Addr, sender)
		return
	}
	// Update address to actual sender (handles NAT port remapping).
	// The client's HTTP-reported port may differ from its NAT-mapped UDP port.
	client.Addr = sender

	// Channel guard: packet channel must match server-recorded channel
	// (client may have stale channel from before a server-side switch)
	if client.Channel != hdr.Channel {
		// Forward anyway but with the server-authoritative channel
		hdr.Channel = client.Channel
		// Re-encode header into the packet so forwarded clients see the correct channel
		hdr.Write(data[:header.Size])
	}

	// Keepalive
	client.Touch()

	// Forward to all other clients in the same channel
	targets := s.mgr.ChannelMembers(client.RoomID, client.Channel)
	for _, target := range targets {
		if target.Token == hdr.Token {
			continue
		}
		// TODO: could skip per-target header re-encode by caching
		_, err := s.conn.WriteToUDP(data, target.Addr)
		if err != nil {
			s.logger.Printf("forward to %v failed: %v", target.Addr, err)
		}
	}

	_ = payloadLen // used for future bandwidth metrics
}

// ipsMatch returns true if two IPs should be considered the same for packet routing.
// Loopback addresses are treated as matching any local-machine address.
func ipsMatch(a, b net.IP) bool {
	if a.Equal(b) {
		return true
	}
	if a.IsLoopback() || b.IsLoopback() {
		return true // same machine, different interface
	}
	return false
}

// Conn returns the underlying UDP connection (for use by tests).
func (s *Server) Conn() *net.UDPConn {
	return s.conn
}

// Close shuts down the UDP listener.
func (s *Server) Close() error {
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
