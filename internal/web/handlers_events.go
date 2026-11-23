package web

import (
	"fmt"
	"net/http"
	"time"
)

// heartbeat keeps proxies from dropping an idle stream.
const heartbeat = 20 * time.Second

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	s.streamEvents(w, r, pollFrom(r.Context()).ID)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, pollID int64) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Connection", "keep-alive")
	// Nginx buffers responses by default, so events would arrive in clumps
	// minutes late.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		// Can't stream without flushing
		return
	}

	sub := &subscriber{pollID: pollID, events: make(chan []byte, 8)}
	select {
	case s.hub.subscribe <- sub:
	case <-r.Context().Done():
		return
	case <-s.hub.done:
		return
	}
	defer func() {
		select {
		case s.hub.unsubscribe <- sub:
		case <-s.hub.done:
		}
	}()

	ping := time.NewTicker(heartbeat)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return // The reader closed the tab
		case <-s.hub.done:
			return // The server is shutting down

		case <-ping.C:
			// SSE comment, keeps the connection open
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}

		case data, ok := <-sub.events:
			if !ok {
				return // The hub let go
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
				return
			}
		}

		if err := rc.Flush(); err != nil {
			return
		}
	}
}

// handleBoard renders just the board for the live refresh, with the same
// template and data as the full page.
func (s *Server) handleBoard(w http.ResponseWriter, r *http.Request) {
	data, err := s.boardData(w, r)
	if err != nil {
		s.fail(w, r, "could not load the board", err)
		return
	}
	s.renderBlock(w, r, "index.html", "board", data)
}
