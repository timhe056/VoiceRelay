package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/boardgame/voicerelay/internal/room"
)

func setupHandler(t *testing.T) *Handler {
	t.Helper()
	mgr := room.NewManager(12)
	return &Handler{mgr: mgr, publicIP: "8.8.8.8", udpPort: 9000}
}

func TestJoinSuccess(t *testing.T) {
	h := setupHandler(t)

	body := `{"roomId":"abc","channel":1,"udpPort":5000}`
	req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h.handleJoin)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var resp JoinResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	if resp.ServerEndpoint != "8.8.8.8" {
		t.Errorf("endpoint = %s, want 8.8.8.8", resp.ServerEndpoint)
	}
	if resp.ServerPort != 9000 {
		t.Errorf("port = %d, want 9000", resp.ServerPort)
	}
	if len(resp.ClientToken) != 32 {
		t.Errorf("token len = %d, want 32 hex chars", len(resp.ClientToken))
	}

	// Verify token is valid hex and 16 bytes
	tokenBytes, _ := hex.DecodeString(resp.ClientToken)
	if len(tokenBytes) != 16 {
		t.Errorf("decoded token len = %d, want 16", len(tokenBytes))
	}

	// Client should be registered
	var token [16]byte
	copy(token[:], tokenBytes)
	client := h.mgr.GetClient(token)
	if client == nil {
		t.Fatal("client not found in room manager")
	}
	if client.Channel != 1 {
		t.Errorf("channel = %d, want 1", client.Channel)
	}
}

func TestJoinInvalidRequest(t *testing.T) {
	h := setupHandler(t)

	tests := []struct {
		name string
		body string
		code int
	}{
		{"missing roomId", `{"channel":1,"udpPort":5000}`, http.StatusBadRequest},
		{"invalid json", `{bad}`, http.StatusBadRequest},
		{"missing udpPort", `{"roomId":"abc","channel":1}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(tt.body))
			req.RemoteAddr = "1.2.3.4:12345"
			rec := httptest.NewRecorder()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/voice/join", h.handleJoin)
			mux.ServeHTTP(rec, req)

			if rec.Code != tt.code {
				t.Errorf("status = %d, want %d", rec.Code, tt.code)
			}
		})
	}
}

func TestJoinRoomFull(t *testing.T) {
	// Fill the room (max 12 per room, but our test manager has maxPerRoom=12)
	// Register 11 clients first, then the 12th should succeed, 13th fail
	// For a faster test, let's use a smaller limit
	smallMgr := room.NewManager(2)
	h2 := &Handler{mgr: smallMgr, publicIP: "8.8.8.8", udpPort: 9000}

	body := `{"roomId":"small","channel":0,"udpPort":5000}`
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h2.handleJoin)

	// Register 2 (fills the room)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(body))
		req.RemoteAddr = "1.2.3.4:12345"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("client %d: status = %d", i, rec.Code)
		}
	}

	// 3rd should be rejected
	req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rec.Code)
	}
}

func TestLeave(t *testing.T) {
	h := setupHandler(t)

	// First join to get a token
	body := `{"roomId":"abc","channel":0,"udpPort":5000}`
	req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h.handleJoin)
	mux.HandleFunc("DELETE /api/voice/leave", h.handleLeave)
	mux.ServeHTTP(rec, req)

	var resp JoinResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	// Now leave
	req2 := httptest.NewRequest("DELETE", "/api/voice/leave?token="+resp.ClientToken, nil)
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Errorf("leave status = %d, want 200", rec2.Code)
	}

	// Second leave should fail
	req3 := httptest.NewRequest("DELETE", "/api/voice/leave?token="+resp.ClientToken, nil)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusNotFound {
		t.Errorf("second leave status = %d, want 404", rec3.Code)
	}
}

func TestLeaveInvalidToken(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/voice/leave", h.handleLeave)

	req := httptest.NewRequest("DELETE", "/api/voice/leave?token=bad", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestChannelSwitch(t *testing.T) {
	h := setupHandler(t)

	// Join
	body := `{"roomId":"abc","channel":0,"udpPort":5000}`
	req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(body))
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h.handleJoin)
	mux.HandleFunc("POST /api/voice/channel", h.handleChannel)
	mux.ServeHTTP(rec, req)

	var resp JoinResponse
	json.NewDecoder(rec.Body).Decode(&resp)

	// Switch channel
	chBody := `{"clientToken":"` + resp.ClientToken + `","newChannel":2}`
	req2 := httptest.NewRequest("POST", "/api/voice/channel", strings.NewReader(chBody))
	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("channel switch status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}

	// Verify token is now in channel 2
	tokenBytes, _ := hex.DecodeString(resp.ClientToken)
	var token [16]byte
	copy(token[:], tokenBytes)
	client := h.mgr.GetClient(token)
	if client == nil || client.Channel != 2 {
		t.Errorf("channel = %d, want 2", client.Channel)
	}
}

func TestStats(t *testing.T) {
	h := setupHandler(t)

	// Register 3 clients across 2 rooms
	body := `{"roomId":"r1","channel":0,"udpPort":5000}`
	body2 := `{"roomId":"r2","channel":0,"udpPort":5000}`

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/voice/join", h.handleJoin)
	mux.HandleFunc("GET /api/voice/stats", h.handleStats)

	join := func(b string) {
		req := httptest.NewRequest("POST", "/api/voice/join", strings.NewReader(b))
		req.RemoteAddr = "1.2.3.4:12345"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	join(body)  // r1
	join(body)  // r1
	join(body2) // r2

	req := httptest.NewRequest("GET", "/api/voice/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var stats StatsResponse
	json.NewDecoder(rec.Body).Decode(&stats)

	if stats.ActiveRooms != 2 {
		t.Errorf("rooms = %d, want 2", stats.ActiveRooms)
	}
	if stats.ActiveClients != 3 {
		t.Errorf("clients = %d, want 3", stats.ActiveClients)
	}
}

func TestHealthz(t *testing.T) {
	h := setupHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.handleHealth)

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("healthy")) {
		t.Error("healthz should contain healthy")
	}
}
