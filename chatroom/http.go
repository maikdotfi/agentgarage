package chatroom

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

// Handler is the chatroom's HTTP API, served on the host's unix socket:
//
//	POST /rooms/{room}/messages          form fields author, text
//	GET  /rooms/{room}/messages?after=N  JSON; add wait=30s to block for news
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /rooms/{room}/messages", func(w http.ResponseWriter, r *http.Request) {
		author, text := r.FormValue("author"), r.FormValue("text")
		if author == "" || text == "" {
			http.Error(w, "author and text are required", http.StatusBadRequest)
			return
		}
		m, err := s.Post(r.Context(), r.PathValue("room"), author, text)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(m)
	})
	mux.HandleFunc("GET /rooms/{room}/messages", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.ParseInt(r.FormValue("after"), 10, 64)
		var msgs []Message
		var err error
		if wait, _ := time.ParseDuration(r.FormValue("wait")); wait > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), wait)
			msgs, err = s.Wait(ctx, r.PathValue("room"), after)
			cancel()
			if errors.Is(err, context.DeadlineExceeded) {
				err = nil
			}
		} else {
			msgs, err = s.Read(r.Context(), r.PathValue("room"), after)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(msgs)
	})
	return mux
}
