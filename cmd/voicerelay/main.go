package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/boardgame/voicerelay/internal/api"
	"github.com/boardgame/voicerelay/internal/relay"
	"github.com/boardgame/voicerelay/internal/room"
)

func main() {
	udpAddr := flag.String("udp", envOrDefault("UDP_PORT", ":9000"), "UDP listen address for voice packets")
	httpAddr := flag.String("http", envOrDefault("HTTP_PORT", ":8080"), "HTTP listen address for signaling API")
	publicIP := flag.String("public-ip", os.Getenv("PUBLIC_IP"), "public IP advertised to clients (auto-detect if empty)")
	maxPerRoom := flag.Int("max-per-room", envOrDefaultInt("MAX_PER_ROOM", 12), "maximum clients per room")
	timeoutMin := flag.Int("timeout-min", envOrDefaultInt("TIMEOUT_MIN", 2), "client timeout in minutes (no packet)")
	cleanupSec := flag.Int("cleanup-sec", envOrDefaultInt("CLEANUP_SEC", 30), "expired client cleanup interval in seconds")
	pprof := flag.Bool("pprof", false, "enable /debug/pprof/ endpoints")
	flag.Parse()

	logger := log.New(os.Stdout, "[voicerelay] ", log.LstdFlags|log.Lmsgprefix)

	// Resolve public IP
	ip := *publicIP
	if ip == "" {
		ip = resolvePublicIP(*udpAddr)
	}
	udpPort := api.MustParseUDPPort((&net.UDPAddr{Port: 0}).String())
	_, portStr, _ := net.SplitHostPort(*udpAddr)
	if p := api.MustParseUDPPort(portStr); p > 0 {
		udpPort = p
	}

	// Room manager
	mgr := room.NewManager(*maxPerRoom)

	// Background cleanup
	stopCleanup := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(*cleanupSec) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				removed := mgr.PurgeExpired(time.Duration(*timeoutMin) * time.Minute)
				if removed > 0 {
					logger.Printf("purged %d expired clients", removed)
				}
			case <-stopCleanup:
				return
			}
		}
	}()

	// UDP relay
	relayServer := relay.New(mgr, logger)
	go func() {
		logger.Printf("UDP relay listening on %s", *udpAddr)
		if err := relayServer.ListenUDP(*udpAddr); err != nil {
			logger.Fatalf("UDP relay failed: %v", err)
		}
	}()

	// HTTP signaling
	handler := api.NewHandler(mgr, ip, udpPort)

	var httpHandler http.Handler
	if *pprof {
		pprofMux := http.NewServeMux()
		pprofMux.Handle("/debug/pprof/", http.DefaultServeMux)
		pprofMux.Handle("/", handler)
		httpHandler = pprofMux
	} else {
		httpHandler = handler
	}

	httpServer := &http.Server{
		Addr:    *httpAddr,
		Handler: httpHandler,
	}

	go func() {
		logger.Printf("HTTP API listening on %s (public IP: %s, UDP port: %d)", *httpAddr, ip, udpPort)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("HTTP server failed: %v", err)
		}
	}()

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig

	logger.Println("shutting down...")
	close(stopCleanup)
	relayServer.Close()
	httpServer.Close()
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envOrDefaultInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func resolvePublicIP(listenAddr string) string {
	// Try to determine the outbound IP by connecting to a public server.
	// Fallback: parse the listen address.
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return fallbackIP(listenAddr)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)
	if localAddr.IP != nil && !localAddr.IP.IsUnspecified() {
		return localAddr.IP.String()
	}
	return fallbackIP(listenAddr)
}

func fallbackIP(listenAddr string) string {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "127.0.0.1"
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		return "127.0.0.1"
	}
	return host
}

