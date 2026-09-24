package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type wsHub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
	logger  *Logger
}

var hub *wsHub
var hubOnce sync.Once

func initWSHub(l *Logger) {
	hubOnce.Do(func() {
		hub = &wsHub{
			clients: make(map[*websocket.Conn]bool),
			logger:  l,
		}
		go hub.broadcastLoop()
	})
}

func handleWebSocket(w http.ResponseWriter, r *http.Request, l *Logger) {
	initWSHub(l)

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}

	hub.mu.Lock()
	hub.clients[conn] = true
	hub.mu.Unlock()

	go func() {
		defer func() {
			hub.mu.Lock()
			delete(hub.clients, conn)
			hub.mu.Unlock()
			conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				break
			}
		}
	}()
}

func (h *wsHub) broadcastLoop() {
	ch := h.logger.Subscribe()
	for entry := range ch {
		summary := entry.ToSummary()
		h.mu.RLock()
		for conn := range h.clients {
			conn.WriteJSON(summary)
		}
		h.mu.RUnlock()
	}
}
