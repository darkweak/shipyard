package api

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"path"
	"time"

	"shipyard/internal/deploy"
)

// Netlify Blobs browser. Store and key go in the query string: both may
// contain slashes.

func (s *Server) listBlobs(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	info, err := s.Deploy.Blobs(ctx, p)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// headerWriter sets the response headers on the first write, so an error
// before any data can still be answered with a JSON error.
type headerWriter struct {
	w       http.ResponseWriter
	started bool
	key     string
}

func (h *headerWriter) Write(p []byte) (int, error) {
	h.start(p)
	return h.w.Write(p)
}

func (h *headerWriter) start(p []byte) {
	if !h.started {
		h.started = true
		hd := h.w.Header()
		hd.Set("Content-Type", http.DetectContentType(p))
		// Blob content is untrusted: never let it run in Shipyard's origin.
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Content-Security-Policy", "sandbox")
		hd.Set("Cache-Control", "no-store")
		name := path.Base(h.key)
		if name == "/" || name == "." {
			name = "blob"
		}
		hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	}
}

func (s *Server) blobContent(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	q := r.URL.Query()
	storeName, key := q.Get("store"), q.Get("key")
	if storeName == "" || key == "" {
		writeError(w, http.StatusBadRequest, "store and key are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	hw := &headerWriter{w: w, key: key}
	err = s.Deploy.BlobContent(ctx, p, storeName, key, hw)
	switch {
	case err == nil && !hw.started: // empty blob
		hw.start(nil)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusOK)
	case err == nil, hw.started:
		// Headers are sent: a failure mid-stream can only cut the response.
	case errors.Is(err, deploy.ErrBlobNotFound):
		writeError(w, http.StatusNotFound, "blob not found")
	case deploy.IsNoBlobs(err):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) deleteBlob(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	q := r.URL.Query()
	if q.Get("store") == "" {
		writeError(w, http.StatusBadRequest, "store is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.Deploy.DeleteBlob(ctx, p, q.Get("store"), q.Get("key")); err != nil {
		if deploy.IsNoBlobs(err) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
