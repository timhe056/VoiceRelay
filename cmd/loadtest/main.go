package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"
)

type JoinResponse struct {
	ClientToken    string `json:"clientToken"`
	ServerEndpoint string `json:"serverEndpoint"`
	ServerPort     int    `json:"serverPort"`
}

type StatsResponse struct {
	ActiveRooms   int `json:"activeRooms"`
	ActiveClients int `json:"activeClients"`
}

func main() {
	n := flag.Int("n", 10, "number of virtual clients")
	rate := flag.Int("rate", 50, "packets per second per client")
	apiURL := flag.String("api", "http://127.0.0.1:8080", "VoiceRelay HTTP API URL")
	roomID := flag.String("room", "loadtest", "room ID")
	dur := flag.Duration("dur", 30*time.Second, "test duration")
	flag.Parse()

	logger := log.New(os.Stdout, "[loadtest] ", log.LstdFlags|log.Lmsgprefix)

	var totalSent, totalRecv atomic.Int64
	serverIP := ""
	serverPort := 0

	type client struct {
		id    int
		token string
		conn  *net.UDPConn
	}

	// ── Join all clients ──
	clients := make([]*client, *n)
	for i := 0; i < *n; i++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
		if err != nil {
			logger.Fatalf("client %d UDP listen: %v", i, err)
		}

		localPort := conn.LocalAddr().(*net.UDPAddr).Port
		token, srvIP, srvPort := join(*apiURL, *roomID, 0, localPort)
		serverIP = srvIP
		serverPort = srvPort

		clients[i] = &client{id: i, token: token, conn: conn}
		if i == 0 {
			logger.Printf("client 0 joined: token=%s... server=%s:%d", token[:16], srvIP, srvPort)
		}
	}
	logger.Printf("%d clients joined room=%s", *n, *roomID)

	serverAddr := &net.UDPAddr{IP: net.ParseIP(serverIP), Port: serverPort}

	// ── Receive goroutine per client ──
	for _, c := range clients {
		go func(cl *client) {
			buf := make([]byte, 2048)
			for {
				n, _, err := cl.conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				if n >= 22 {
					// Count valid voice packets (ignore tiny/unparseable)
					totalRecv.Add(1)
				}
			}
		}(c)
	}

	// ── Send goroutine per client ──
	silence := make([]byte, 40) // ~20ms Opus silence frame
	stop := make(chan struct{})
	interval := time.Second / time.Duration(*rate)

	for _, c := range clients {
		go func(cl *client) {
			tokenBytes, _ := hex.DecodeString(cl.token)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			var seq uint32
			for {
				select {
				case <-ticker.C:
					pkt := buildPacket(tokenBytes, seq, 0, 0, silence)
					cl.conn.WriteToUDP(pkt, serverAddr)
					seq++
					totalSent.Add(1)
				case <-stop:
					return
				}
			}
		}(c)
	}

	// ── Progress reporter ──
	go func() {
		prevSent := int64(0)
		prevRecv := int64(0)
		for range time.Tick(2 * time.Second) {
			s := totalSent.Load()
			r := totalRecv.Load()
			sRate := (s - prevSent) / 2
			rRate := (r - prevRecv) / 2
			prevSent = s
			prevRecv = r

			rooms, active := fetchStats(*apiURL)
			logger.Printf("sent=%d (+%d/s) recv=%d (+%d/s) rooms=%d clients=%d",
				s, sRate, r, rRate, rooms, active)
		}
	}()

	// ── Run ──
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	logger.Printf("Running for %v (Ctrl+C to stop early)...", *dur)
	select {
	case <-time.After(*dur):
	case <-sigCh:
		logger.Printf("interrupted, stopping...")
	}
	close(stop)

	// ── Summary ──
	sent := totalSent.Load()
	recv := totalRecv.Load()
	rooms, active := fetchStats(*apiURL)

	fmt.Println()
	fmt.Println("========== Load Test Results ==========")
	fmt.Printf("  Duration      : %v\n", *dur)
	fmt.Printf("  Clients       : %d\n", *n)
	fmt.Printf("  Target rate   : %d pps/client\n", *rate)
	fmt.Printf("  Packets sent  : %d (%.0f pps avg)\n", sent, float64(sent)/dur.Seconds())
	fmt.Printf("  Packets recv  : %d (%.0f pps avg)\n", recv, float64(recv)/dur.Seconds())

	expectedRecv := sent * int64(*n-1)
	if expectedRecv > 0 {
		loss := (1 - float64(recv)/float64(expectedRecv)) * 100
		fmt.Printf("  Expected recv : %d\n", expectedRecv)
		fmt.Printf("  Loss rate     : %.4f%%\n", loss)
	}
	fmt.Printf("  Server rooms  : %d\n", rooms)
	fmt.Printf("  Server clients: %d\n", active)
	fmt.Println("=======================================")

	// Cleanup — leave all clients
	logger.Printf("cleaning up %d clients...", *n)
	for _, c := range clients {
		leave(*apiURL, c.token)
		c.conn.Close()
	}
	logger.Printf("cleanup done")
}

func join(apiURL, roomID string, channel, localPort int) (string, string, int) {
	body := fmt.Sprintf(`{"roomId":"%s","displayName":"loadtest","channel":%d,"udpPort":%d}`, roomID, channel, localPort)
	resp, err := http.Post(apiURL+"/api/voice/join", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		log.Fatalf("Join failed: %v", err)
	}
	defer resp.Body.Close()

	var jr JoinResponse
	json.NewDecoder(resp.Body).Decode(&jr)
	if jr.ClientToken == "" {
		log.Fatalf("Join returned no token (status %d)", resp.StatusCode)
	}
	return jr.ClientToken, jr.ServerEndpoint, jr.ServerPort
}

func leave(apiURL, token string) {
	req, _ := http.NewRequest("DELETE", apiURL+"/api/voice/leave?token="+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

func fetchStats(apiURL string) (int, int) {
	resp, err := http.Get(apiURL + "/api/voice/stats")
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()
	var s StatsResponse
	json.NewDecoder(resp.Body).Decode(&s)
	return s.ActiveRooms, s.ActiveClients
}

func buildPacket(token []byte, seq uint32, channel byte, codec byte, payload []byte) []byte {
	pkt := make([]byte, 22+len(payload))
	copy(pkt[0:16], token)
	pkt[16] = byte(seq >> 24)
	pkt[17] = byte(seq >> 16)
	pkt[18] = byte(seq >> 8)
	pkt[19] = byte(seq)
	pkt[20] = channel
	pkt[21] = codec
	copy(pkt[22:], payload)
	return pkt
}
