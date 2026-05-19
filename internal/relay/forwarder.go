// Package relay implements the core voice packet forwarding engine.
package relay

import (
	"log"
	"net"
	"sync"

	"github.com/boardgame/voicerelay/internal/header"
	"github.com/boardgame/voicerelay/internal/room"
)

// Server holds the UDP listener and routes voice packets.
type Server struct {
	conn   *net.UDPConn
	mgr    *room.Manager
	logger *log.Logger

	workers   int
	packetCh  chan packetJob
	closeCh   chan struct{}
	closeOnce sync.Once

	bufPool sync.Pool
}

type packetJob struct {
	data   []byte
	sender *net.UDPAddr
}

// New creates a Server with the given room manager.
func New(mgr *room.Manager, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{
		mgr:      mgr,
		logger:   logger,
		workers:  8,
		packetCh: make(chan packetJob, 2048),
		closeCh:  make(chan struct{}),
	}
	s.bufPool.New = func() interface{} { return make([]byte, 2048) }
	return s
}

// ListenUDP binds to addr (e.g. ":9000") and starts the receive loop.
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

	// Start worker pool
	for i := 0; i < s.workers; i++ {
		go s.worker()
	}

	buf := make([]byte, 2048)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			select {
			case <-s.closeCh:
				return nil
			default:
				s.logger.Printf("UDP read error: %v", err)
				continue
			}
		}

		// Copy data via buffer pool to avoid per-packet allocation
		poolBuf := s.bufPool.Get().([]byte)
		copyLen := n
		if copyLen > len(poolBuf) {
			copyLen = len(poolBuf)
		}
		copy(poolBuf[:copyLen], buf[:n])

		job := packetJob{data: poolBuf[:copyLen], sender: remoteAddr}
		select {
		case s.packetCh <- job:
		default:
			// Channel full — drop packet
			s.bufPool.Put(poolBuf)
		}
	}
}

func (s *Server) worker() {
	for {
		select {
		case job := <-s.packetCh:
			s.handlePacket(job.data, job.sender)
			s.bufPool.Put(job.data[:cap(job.data)])
		case <-s.closeCh:
			return
		}
	}
}

// handlePacket processes a single incoming voice packet.
func (s *Server) handlePacket(data []byte, sender *net.UDPAddr) {
	if len(data) < header.Size {
		return
	}

	hdr, err := header.Parse(data[:header.Size])
	if err != nil {
		return
	}

	client := s.mgr.GetClient(hdr.Token)
	if client == nil {
		return
	}

	// Update address on every packet (NAT rebinding)
	client.Addr = sender

	// Channel guard
	if client.Channel != hdr.Channel {
		hdr.Channel = client.Channel
		hdr.Write(data[:header.Size])
	}

	client.Touch()

	// Forward to all other clients in same channel
	targets := s.mgr.ChannelMembers(client.RoomID, client.Channel)
	for _, target := range targets {
		if target.Token == hdr.Token {
			continue
		}
		_, err := s.conn.WriteToUDP(data, target.Addr)
		if err != nil {
			s.logger.Printf("forward to %v failed: %v", target.Addr, err)
		}
	}
}

// Conn returns the underlying UDP connection.
func (s *Server) Conn() *net.UDPConn { return s.conn }

// Close shuts down the UDP listener and workers.
func (s *Server) Close() error {
	s.closeOnce.Do(func() { close(s.closeCh) })
	if s.conn != nil {
		return s.conn.Close()
	}
	return nil
}
