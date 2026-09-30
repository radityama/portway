package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"
)

type Event struct {
	Event     string `json:"event"`
	LocalURL  string `json:"local_url,omitempty"`
	PublicURL string `json:"public_url,omitempty"`
	Relay     string `json:"relay,omitempty"`
	Port      int    `json:"port,omitempty"`
	Error     string `json:"error,omitempty"`
}

func main() {
	port := 0
	if len(os.Args) > 1 {
		parsed, err := strconv.Atoi(os.Args[1])
		if err != nil || parsed < 1 || parsed > 65535 {
			fatal("invalid port")
		}
		port = parsed
	}
	if port == 0 {
		fmt.Fprintln(os.Stderr, "usage: portway <port>")
		os.Exit(2)
	}

	if !localReachable(port) {
		fatal(fmt.Sprintf("localhost:%d is not reachable", port))
	}

	event := Event{Event: "ready", LocalURL: fmt.Sprintf("http://127.0.0.1:%d", port), Port: port}
	if os.Getenv("PORTWAY_JSON") == "1" {
		_ = json.NewEncoder(os.Stdout).Encode(event)
		return
	}

	fmt.Printf("✓ Local   %s\n", event.LocalURL)
	fmt.Println("○ Portway starter mode (relay registration not implemented yet)")
	fmt.Println("\nRead docs/IMPLEMENTATION.md and implement Phase 1–3 next.")

}

func localReachable(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func fatal(message string) {
	if os.Getenv("PORTWAY_JSON") == "1" {
		_ = json.NewEncoder(os.Stdout).Encode(Event{Event: "error", Error: message})
	} else {
		fmt.Fprintln(os.Stderr, "error:", message)
	}
	os.Exit(1)
}
