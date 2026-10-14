package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"shipyard/internal/store"
)

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.ListDeployments(r.Context(), r.PathValue("id"), 50)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.GetDeployment(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.Deploy.Cancel(r.PathValue("id")) {
		writeError(w, http.StatusConflict, "deployment is not running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (s *Server) redeploy(w http.ResponseWriter, r *http.Request) {
	d, err := s.Deploy.Rollback(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, d)
}

// streamDeploymentLogs sends the log as Server-Sent Events: one "line" event
// per log line (JSON string), then a final "end" event.
func (s *Server) streamDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	stream := s.Hub.Get(id)
	var persisted string
	if stream == nil {
		var err error
		if persisted, err = s.Store.GetDeploymentLog(r.Context(), id); err != nil {
			writeStoreError(w, err)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(event, data string) {
		b, _ := json.Marshal(data)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}

	if stream == nil {
		if persisted != "" {
			for _, l := range strings.Split(persisted, "\n") {
				send("line", l)
			}
		}
		send("end", "")
		flusher.Flush()
		return
	}

	history, ch, unsubscribe := stream.Subscribe()
	defer unsubscribe()
	for _, l := range history {
		send("line", l)
	}
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		case l, ok := <-ch:
			if !ok {
				// Either the deployment ended or we fell behind; the client
				// reconnects in the latter case and gets the full history.
				if d, err := s.Store.GetDeployment(r.Context(), id); err == nil && store.IsFinal(d.Status) {
					send("end", "")
				}
				flusher.Flush()
				return
			}
			send("line", l)
			// Drain whatever else is ready before flushing.
			for drained := false; !drained; {
				select {
				case l, ok := <-ch:
					if !ok {
						drained = true
						break
					}
					send("line", l)
				default:
					drained = true
				}
			}
			flusher.Flush()
		}
	}
}
