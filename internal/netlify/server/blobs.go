package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// blobs serves the Netlify Blobs edge API on loopback so @netlify/blobs works
// inside functions: the runtime gets NETLIFY_BLOBS_CONTEXT pointing here.
// Each blob is two files under dir/<sha256(store)>/: <sha256(key)>.data and a
// .json sidecar holding the key, etag and metadata.
type blobs struct {
	dir, siteID, token string
	mu                 sync.Mutex // serialises writes (conditional writes read then write)
}

type blobMeta struct {
	Key      string    `json:"key"`
	ETag     string    `json:"etag"`
	Metadata string    `json:"metadata,omitempty"` // x-amz-meta-user, as the client encoded it
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

const (
	blobsSiteID     = "shipyard"
	blobsMetaHeader = "x-amz-meta-user"
	blobsMaxSize    = 64 << 20
)

func newBlobs(dir string) (*blobs, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tok := make([]byte, 24)
	if _, err := rand.Read(tok); err != nil {
		return nil, err
	}
	return &blobs{dir: dir, siteID: blobsSiteID, token: hex.EncodeToString(tok)}, nil
}

// start listens on a random loopback port and returns the value of
// NETLIFY_BLOBS_CONTEXT for the functions runtime.
func (b *blobs) start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: b, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("blobs server: %v", err)
		}
	}()
	url := "http://" + ln.Addr().String()
	ctx := map[string]string{
		"edgeURL": url, "uncachedEdgeURL": url,
		"siteID": b.siteID, "token": b.token, "primaryRegion": "local",
	}
	if id := os.Getenv("DEPLOY_ID"); id != "" {
		ctx["deployID"] = id
	}
	j, _ := json.Marshal(ctx)
	log.Printf("blobs: storing in %s", b.dir)
	return base64.StdEncoding.EncodeToString(j), nil
}

func hashName(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (b *blobs) storeDir(store string) string { return filepath.Join(b.dir, hashName(store)) }

func (b *blobs) paths(store, key string) (data, meta string) {
	base := filepath.Join(b.storeDir(store), hashName(key))
	return base + ".data", base + ".json"
}

func (b *blobs) readMeta(file string) (*blobMeta, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var m blobMeta
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeAtomic(file string, r io.Reader) (int64, []byte, error) {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return 0, nil, err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, nil, err
	}
	return n, h.Sum(nil), os.Rename(tmp.Name(), file)
}

func (b *blobs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+b.token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// /[region:<r>/]<siteID>[/<store>[/<key…>]]
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
	if strings.HasPrefix(parts[0], "region:") {
		parts = strings.SplitN(strings.Join(parts[1:], "/"), "/", 3)
	} else if len(parts) == 4 {
		parts = []string{parts[0], parts[1], parts[2] + "/" + parts[3]}
	}
	if parts[0] != b.siteID {
		http.Error(w, "unknown site", http.StatusNotFound)
		return
	}
	switch {
	case len(parts) == 1 || parts[1] == "":
		b.listStores(w, r)
	case len(parts) == 2 || parts[2] == "":
		if r.Method == http.MethodDelete {
			b.deleteAll(w, parts[1])
		} else {
			b.list(w, r, parts[1])
		}
	default:
		b.blob(w, r, parts[1], parts[2])
	}
}

func (b *blobs) blob(w http.ResponseWriter, r *http.Request, store, key string) {
	dataFile, metaFile := b.paths(store, key)
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		m, err := b.readMeta(metaFile)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("ETag", m.ETag)
		w.Header().Set("Last-Modified", m.Modified.UTC().Format(http.TimeFormat))
		if m.Metadata != "" {
			w.Header().Set(blobsMetaHeader, m.Metadata)
		}
		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == m.ETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		f, err := os.Open(dataFile)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.Copy(w, f)
	case http.MethodPut:
		b.mu.Lock()
		defer b.mu.Unlock()
		cur, _ := b.readMeta(metaFile)
		if im := r.Header.Get("If-Match"); im != "" && (cur == nil || cur.ETag != im) {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if r.Header.Get("If-None-Match") == "*" && cur != nil {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		if err := os.MkdirAll(filepath.Dir(dataFile), 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		storeFile := filepath.Join(filepath.Dir(dataFile), "store")
		if _, err := os.Stat(storeFile); errors.Is(err, fs.ErrNotExist) {
			_ = os.WriteFile(storeFile, []byte(store), 0o644)
		}
		n, sum, err := writeAtomic(dataFile, http.MaxBytesReader(w, r.Body, blobsMaxSize))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		m := blobMeta{Key: key, ETag: `"` + hex.EncodeToString(sum[:16]) + `"`, Metadata: r.Header.Get(blobsMetaHeader), Size: n, Modified: time.Now()}
		j, _ := json.Marshal(m)
		if _, _, err := writeAtomic(metaFile, strings.NewReader(string(j))); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("ETag", m.ETag)
		w.WriteHeader(http.StatusOK)
	case http.MethodDelete:
		b.mu.Lock()
		defer b.mu.Unlock()
		_ = os.Remove(metaFile)
		_ = os.Remove(dataFile)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (b *blobs) entries(store string) []*blobMeta {
	files, _ := filepath.Glob(filepath.Join(b.storeDir(store), "*.json"))
	var out []*blobMeta
	for _, f := range files {
		if m, err := b.readMeta(f); err == nil {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func (b *blobs) list(w http.ResponseWriter, r *http.Request, store string) {
	prefix := r.URL.Query().Get("prefix")
	dirs := r.URL.Query().Get("directories") == "true"
	type item struct {
		Key          string `json:"key"`
		ETag         string `json:"etag"`
		LastModified string `json:"last_modified"`
		Size         int64  `json:"size"`
	}
	res := struct {
		Blobs       []item   `json:"blobs"`
		Directories []string `json:"directories"`
	}{Blobs: []item{}, Directories: []string{}}
	seen := map[string]bool{}
	for _, m := range b.entries(store) {
		if !strings.HasPrefix(m.Key, prefix) {
			continue
		}
		if dirs {
			if i := strings.Index(m.Key[len(prefix):], "/"); i >= 0 {
				if d := m.Key[:len(prefix)+i]; !seen[d] {
					seen[d] = true
					res.Directories = append(res.Directories, d)
				}
				continue
			}
		}
		res.Blobs = append(res.Blobs, item{m.Key, m.ETag, m.Modified.UTC().Format(time.RFC3339), m.Size})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}

func (b *blobs) deleteAll(w http.ResponseWriter, store string) {
	b.mu.Lock()
	n := len(b.entries(store))
	err := os.RemoveAll(b.storeDir(store))
	b.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"blobs_deleted": n, "has_more": false})
}

func (b *blobs) listStores(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	stores := []string{}
	names, _ := filepath.Glob(filepath.Join(b.dir, "*", "store"))
	for _, f := range names {
		if s, err := os.ReadFile(f); err == nil && strings.HasPrefix(string(s), prefix) {
			stores = append(stores, string(s))
		}
	}
	sort.Strings(stores)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"stores": stores})
}
