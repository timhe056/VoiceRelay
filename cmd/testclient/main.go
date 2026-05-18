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
	"time"
)

type JoinResponse struct {
	ClientToken    string `json:"clientToken"`
	ServerEndpoint string `json:"serverEndpoint"`
	ServerPort     int    `json:"serverPort"`
}

func main() {
	apiURL := flag.String("api", "http://127.0.0.1:8080", "VoiceRelay HTTP API URL")
	roomID := flag.String("room", "test-room", "room ID")
	channel := flag.Int("channel", 0, "voice channel")
	flag.Parse()

	// Bind a single UDP socket for the entire session
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("0.0.0.0"), Port: 0})
	if err != nil {
		log.Fatalf("UDP listen: %v", err)
	}
	defer conn.Close()
	localPort := conn.LocalAddr().(*net.UDPAddr).Port

	// Join via HTTP
	token, serverIP, serverPort := join(*apiURL, *roomID, *channel, localPort)
	fmt.Printf("Joined: token=%s... server=%s:%d localPort=%d\n", token[:16], serverIP, serverPort, localPort)

	tokenBytes, _ := hex.DecodeString(token)
	serverAddr := &net.UDPAddr{IP: net.ParseIP(serverIP), Port: serverPort}

	// Receive loop — print stats every second
	go func() {
		buf := make([]byte, 2048)
		count := 0
		lastPrint := time.Now()
		for {
			n, addr, _ := conn.ReadFromUDP(buf)
			count++
			if time.Since(lastPrint) >= 1*time.Second {
				fmt.Printf("  RECV: %d packets (last %d bytes from %v)\n", count, n, addr)
				lastPrint = time.Now()
			}
		}
	}()

	// Send a silence frame every 2 seconds so server sees us alive
	var seq uint32
	go func() {
		for range time.Tick(2 * time.Second) {
			pkt := buildPacket(tokenBytes, seq, byte(*channel), 0, []byte{0xFC, 0xFF, 0xFE})
			conn.WriteToUDP(pkt, serverAddr)
			seq++
		}
	}()

	fmt.Println("Listening for voice packets... (Ctrl+C to stop)")
	select {} // run forever
}

func join(apiURL, roomID string, channel, localPort int) (string, string, int) {
	body := fmt.Sprintf(`{"roomId":"%s","channel":%d,"udpPort":%d}`, roomID, channel, localPort)
	resp, err := http.Post(apiURL+"/api/voice/join", "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		log.Fatalf("Join failed: %v", err)
	}
	defer resp.Body.Close()

	var jr JoinResponse
	json.NewDecoder(resp.Body).Decode(&jr)

	if jr.ClientToken == "" {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		log.Fatalf("Join returned no token (status %d): %s", resp.StatusCode, buf.String())
	}

	return jr.ClientToken, jr.ServerEndpoint, jr.ServerPort
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
