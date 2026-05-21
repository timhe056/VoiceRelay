// Package api provides the HTTP signaling endpoints for VoiceRelay.
// Clients call these endpoints before establishing UDP voice connections.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/boardgame/voicerelay/internal/room"
)

// Handler wraps the voice API HTTP handlers and the room manager.
type Handler struct {
	mgr        *room.Manager
	publicIP   string // advertised server IP for clients
	udpPort    int    // UDP port clients should send voice to
}

// NewHandler creates an http.Handler with all voice API routes.
func NewHandler(mgr *room.Manager, publicIP string, udpPort int) http.Handler {
	h := &Handler{mgr: mgr, publicIP: publicIP, udpPort: udpPort}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h.handleJoin)
	mux.HandleFunc("DELETE /api/voice/leave", h.handleLeave)
	mux.HandleFunc("POST /api/voice/channel", h.handleChannel)
	mux.HandleFunc("GET /api/voice/room/{roomId}/members", h.handleRoomMembers)
	mux.HandleFunc("GET /api/voice/stats", h.handleStats)
	mux.HandleFunc("GET /healthz", h.handleHealth)
	return mux
}

// ── Request / Response types ──────────────────────────────────────────

type JoinRequest struct {
	RoomID      string `json:"roomId"`
	DisplayName string `json:"displayName,omitempty"`
	Channel     byte   `json:"channel"` // 0=Lobby, 1=Day, 2=Witch, 3=Dead
	UDPPort     int    `json:"udpPort"` // client's own UDP listen port (NAT traversal)
}

type JoinResponse struct {
	ClientToken    string `json:"clientToken"`    // hex-encoded 16-byte token
	ServerEndpoint string `json:"serverEndpoint"` // relay server IP
	ServerPort     int    `json:"serverPort"`     // relay server UDP port
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type ChannelRequest struct {
	ClientToken string `json:"clientToken"`
	NewChannel  byte   `json:"newChannel"`
}

type RoomMembersResponse struct {
	RoomID  string             `json:"roomId"`
	Members []room.MemberInfo  `json:"members"`
}

type StatsResponse struct {
	ActiveRooms   int `json:"activeRooms"`
	ActiveClients int `json:"activeClients"`
}

// ── Handlers ──────────────────────────────────────────────────────────

func (h *Handler) handleJoin(w http.ResponseWriter, r *http.Request) {
	var req JoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}
	if req.RoomID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "roomId is required"})
		return
	}

	token := generateToken()
	// Address is initially nil — it will be learned from the first UDP packet
	// (see forwarder.go handlePacket). Using HTTP source IP + reported UDP port
	// is incorrect when the client is behind NAT, because the NAT assigns a
	// different public port for UDP traffic.
	client, ok := h.mgr.RegisterClient(req.RoomID, req.DisplayName, req.Channel, nil, token)
	if !ok {
		writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "room is full"})
		return
	}
	_ = client

	resp := JoinResponse{
		ClientToken:    hex.EncodeToString(token[:]),
		ServerEndpoint: h.publicIP,
		ServerPort:     h.udpPort,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleLeave(w http.ResponseWriter, r *http.Request) {
	tokenStr := r.URL.Query().Get("token")
	if tokenStr == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "token is required"})
		return
	}
	tokenBytes, err := hex.DecodeString(tokenStr)
	if err != nil || len(tokenBytes) != 16 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid token"})
		return
	}
	var token [16]byte
	copy(token[:], tokenBytes)

	if h.mgr.UnregisterClient(token) {
		w.WriteHeader(http.StatusOK)
	} else {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "token not found"})
	}
}

func (h *Handler) handleChannel(w http.ResponseWriter, r *http.Request) {
	var req ChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}

	tokenBytes, err := hex.DecodeString(req.ClientToken)
	if err != nil || len(tokenBytes) != 16 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid clientToken"})
		return
	}
	var token [16]byte
	copy(token[:], tokenBytes)

	if h.mgr.UpdateChannel(token, req.NewChannel) {
		w.WriteHeader(http.StatusOK)
	} else {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "token not found"})
	}
}

func (h *Handler) handleRoomMembers(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if roomID == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "roomId is required"})
		return
	}
	members := h.mgr.ListRoomMembers(roomID)
	writeJSON(w, http.StatusOK, RoomMembersResponse{
		RoomID:  roomID,
		Members: members,
	})
}

func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	rooms, clients := h.mgr.Stats()
	writeJSON(w, http.StatusOK, StatsResponse{
		ActiveRooms:   rooms,
		ActiveClients: clients,
	})
}

func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

// ── Helpers ───────────────────────────────────────────────────────────

func generateToken() [16]byte {
	var token [16]byte
	_, _ = rand.Read(token[:])
	return token
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// extractPublicIP tries to get the public-facing IP from the request context.
// Defaults to "127.0.0.1" if the TCP listener address can't be determined.
func ExtractPublicIP(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
}

// FormatUDPPort extracts the port number from a net.Addr string.
func FormatUDPPort(addr net.Addr) string {
	_, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "0"
	}
	return port
}

// MustParseUDPPort converts a string port to int, panics on error (for startup).
func MustParseUDPPort(portStr string) int {
	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0
	}
	return p
}
