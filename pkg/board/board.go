package board

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sync"
)

// Board holds the application state.
type Board struct {
	config   Config
	store    Store
	sessions SessionManager
	poller   *Poller
	mu       sync.RWMutex
	clients  map[chan string]struct{}
}

func newBoard(ctx context.Context, cfg Config, store Store, sessions SessionManager) *Board {
	// Seed default columns if the table is empty.
	cols, _ := store.ListColumns()
	if len(cols) == 0 {
		_ = store.SeedColumns(defaultColumns)
	}

	b := &Board{
		config:   cfg,
		store:    store,
		sessions: sessions,
		clients:  make(map[chan string]struct{}),
	}

	b.poller = newPoller(store, sessions, b.broadcast)
	go b.poller.Run(ctx)

	return b
}

// broadcast sends an SSE refresh event to all connected clients.
func (b *Board) broadcast() {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- "refresh":
		default:
		}
	}
}

// --- Column helpers ---

func nextColumn(cols []Column, current string) string {
	if i := columnIndex(cols, current); i >= 0 && i+1 < len(cols) {
		return cols[i+1].ID
	}
	return ""
}

func columnPrompt(cols []Column, colID string) string {
	if i := columnIndex(cols, colID); i >= 0 {
		return cols[i].Prompt
	}
	return ""
}

func columnIndex(cols []Column, colID string) int {
	return slices.IndexFunc(cols, func(c Column) bool { return c.ID == colID })
}

// --- SSE handler ---

func (b *Board) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 16)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.clients, ch)
		b.mu.Unlock()
	}()

	// Send initial event
	_, _ = fmt.Fprintf(w, "data: refresh\n\n")
	flusher.Flush()

	for {
		select {
		case msg := <-ch:
			_, _ = fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
