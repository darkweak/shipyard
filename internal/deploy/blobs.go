package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"shipyard/internal/netlify"
	"shipyard/internal/store"
)

// Netlify Blobs live in the project's blobs volume, laid out by the
// netlify-server (internal/netlify/server/blobs.go): dir/<sha256(store)>/
// holds a "store" file with the store's name and, per blob,
// <sha256(key)>.data plus a <sha256(key)>.json sidecar. Shipyard reads that
// volume through a throwaway container of the project's live image (which
// ships sh, cat and rm whenever the site has functions), so this works
// without access to the daemon's filesystem.

type Blob struct {
	Key      string    `json:"key"`
	ETag     string    `json:"etag"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
	// Metadata is the JSON object the function attached, when it decodes.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

type BlobStore struct {
	Name  string `json:"name"`
	Blobs []Blob `json:"blobs"`
}

type BlobsInfo struct {
	// Enabled is false when the project can't have blobs (Reason says why).
	Enabled bool        `json:"enabled"`
	Reason  string      `json:"reason,omitempty"`
	Volume  string      `json:"volume"`
	Stores  []BlobStore `json:"stores"`
}

// ErrBlobNotFound is returned when the store or the key doesn't exist.
var ErrBlobNotFound = errors.New("blob not found")

// errNoBlobs explains why a project has nothing to browse.
type errNoBlobs string

func (e errNoBlobs) Error() string { return string(e) }

func blobHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// blobsImage picks the image to inspect the volume with: the live deployment's.
func (m *Manager) blobsImage(ctx context.Context, p *store.Project) (string, error) {
	if p.BuildType != store.BuildNetlify {
		return "", errNoBlobs("Netlify Blobs are only available for projects built from netlify.toml.")
	}
	ds, err := m.store.ListDeployments(ctx, p.ID, 50)
	if err != nil {
		return "", err
	}
	image := ""
	for _, d := range ds {
		if d.Status == store.StatusSucceeded && d.Image != "" {
			image = d.Image
			break
		}
	}
	if image == "" {
		return "", errNoBlobs("Deploy the project first: blobs are written by its functions.")
	}
	if !m.docker.ImageExists(ctx, image) {
		return "", errNoBlobs("The live image " + image + " isn't on this Docker host anymore; redeploy to browse blobs.")
	}
	ep, err := m.docker.ImageEntrypoint(ctx, image)
	if err != nil {
		return "", err
	}
	if !slices.Contains(ep, "-blobs") {
		return "", errNoBlobs("The live deployment has no Netlify Functions, or was built before Blobs support: blobs are only available to functions (redeploy if the site has some).")
	}
	return image, nil
}

func (m *Manager) blobsMount(p *store.Project, readOnly bool) []string {
	v := BlobsVolume(p) + ":" + netlify.BlobsDir
	if readOnly {
		v += ":ro"
	}
	return []string{v}
}

// listScript prints "path\0content\0" for every store name and blob sidecar.
const listScript = `cd "$1" 2>/dev/null || exit 0
for f in */store */*.json; do
  [ -f "$f" ] || continue
  printf '%s\0' "$f"; cat "$f"; printf '\0'
done`

// Blobs lists every store of the project and its blobs' metadata.
func (m *Manager) Blobs(ctx context.Context, p *store.Project) (*BlobsInfo, error) {
	info := &BlobsInfo{Volume: BlobsVolume(p), Stores: []BlobStore{}}
	image, err := m.blobsImage(ctx, p)
	var no errNoBlobs
	if errors.As(err, &no) {
		info.Reason = string(no)
		return info, nil
	}
	if err != nil {
		return nil, err
	}
	info.Enabled = true
	var out bytes.Buffer
	if err := m.docker.RunOnce(ctx, image, m.blobsMount(p, true), &out, "sh", "-c", listScript, "sh", netlify.BlobsDir); err != nil {
		return nil, err
	}
	info.Stores = parseBlobListing(out.Bytes())
	return info, nil
}

func parseBlobListing(raw []byte) []BlobStore {
	parts := bytes.Split(raw, []byte{0})
	names := map[string]string{} // store dir -> name
	blobs := map[string][]Blob{} // store dir -> blobs
	for i := 0; i+1 < len(parts); i += 2 {
		dir, file, ok := strings.Cut(string(parts[i]), "/")
		if !ok {
			continue
		}
		if file == "store" {
			names[dir] = string(parts[i+1])
			continue
		}
		var meta struct {
			Key      string    `json:"key"`
			ETag     string    `json:"etag"`
			Metadata string    `json:"metadata"`
			Size     int64     `json:"size"`
			Modified time.Time `json:"modified"`
		}
		if json.Unmarshal(parts[i+1], &meta) != nil {
			continue
		}
		blobs[dir] = append(blobs[dir], Blob{Key: meta.Key, ETag: meta.ETag, Size: meta.Size, Modified: meta.Modified, Metadata: decodeBlobMetadata(meta.Metadata)})
	}
	stores := []BlobStore{}
	for dir, name := range names {
		bs := blobs[dir]
		if bs == nil {
			bs = []Blob{}
		}
		sort.Slice(bs, func(i, j int) bool { return bs[i].Key < bs[j].Key })
		stores = append(stores, BlobStore{Name: name, Blobs: bs})
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].Name < stores[j].Name })
	return stores
}

// decodeBlobMetadata decodes the x-amz-meta-user value @netlify/blobs sends
// ("b64;" + base64 of a JSON object).
func decodeBlobMetadata(s string) json.RawMessage {
	enc, ok := strings.CutPrefix(s, "b64;")
	if !ok {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || !json.Valid(raw) || string(raw) == "{}" {
		return nil
	}
	return raw
}

func blobPath(store, key string) string {
	return netlify.BlobsDir + "/" + blobHash(store) + "/" + blobHash(key)
}

// BlobContent streams a blob's data to w. It returns ErrBlobNotFound
// without writing anything when the blob doesn't exist.
func (m *Manager) BlobContent(ctx context.Context, p *store.Project, storeName, key string, w io.Writer) error {
	image, err := m.blobsImage(ctx, p)
	if err != nil {
		return err
	}
	cw := &countWriter{w: w}
	err = m.docker.RunOnce(ctx, image, m.blobsMount(p, true), cw, "cat", blobPath(storeName, key)+".data")
	if err != nil && cw.n == 0 && strings.Contains(err.Error(), "No such file") {
		return ErrBlobNotFound
	}
	return err
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// DeleteBlob removes one blob, or the whole store when key is empty.
func (m *Manager) DeleteBlob(ctx context.Context, p *store.Project, storeName, key string) error {
	image, err := m.blobsImage(ctx, p)
	if err != nil {
		return err
	}
	if storeName == "" {
		return fmt.Errorf("store is required")
	}
	args := []string{"-rf", netlify.BlobsDir + "/" + blobHash(storeName)}
	if key != "" {
		base := blobPath(storeName, key)
		args = []string{"-f", base + ".json", base + ".data"}
	}
	return m.docker.RunOnce(ctx, image, m.blobsMount(p, false), io.Discard, "rm", args...)
}

// ErrBlobChanged is returned by PutBlob when the blob's ETag no longer matches.
var ErrBlobChanged = errors.New("the blob changed since it was loaded")

// BlobMaxSize matches the netlify-server's limit on blob writes.
const BlobMaxSize = 64 << 20

// blobSidecar is the .json file the netlify-server keeps next to each blob.
type blobSidecar struct {
	Key      string    `json:"key"`
	ETag     string    `json:"etag"`
	Metadata string    `json:"metadata,omitempty"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

// writeScript replaces a blob atomically: data from stdin, sidecar from $4.
// Temporary files don't end in .json so listings never pick them up.
const writeScript = `set -e
d="$1"; b="$2"
mkdir -p "$d"
[ -f "$d/store" ] || printf '%s' "$3" > "$d/store"
cat > "$d/.tmp-shipyard-data"
printf '%s' "$4" > "$d/.tmp-shipyard-meta"
mv -f "$d/.tmp-shipyard-data" "$b.data"
mv -f "$d/.tmp-shipyard-meta" "$b.json"`

// PutBlob replaces a blob's data, keeping its metadata. With ifMatch set, it
// fails with ErrBlobChanged unless the current ETag is ifMatch.
func (m *Manager) PutBlob(ctx context.Context, p *store.Project, storeName, key string, data []byte, ifMatch string) (*Blob, error) {
	image, err := m.blobsImage(ctx, p)
	if err != nil {
		return nil, err
	}
	if storeName == "" || key == "" {
		return nil, fmt.Errorf("store and key are required")
	}
	if len(data) > BlobMaxSize {
		return nil, fmt.Errorf("blobs are limited to %d MB", BlobMaxSize>>20)
	}
	base := blobPath(storeName, key)
	var cur bytes.Buffer
	var meta blobSidecar
	err = m.docker.RunOnce(ctx, image, m.blobsMount(p, true), &cur, "sh", "-c", `[ ! -f "$1" ] || cat "$1"`, "sh", base+".json")
	if err != nil {
		return nil, err
	}
	exists := cur.Len() > 0 && json.Unmarshal(cur.Bytes(), &meta) == nil
	if ifMatch != "" && (!exists || meta.ETag != ifMatch) {
		return nil, ErrBlobChanged
	}

	sum := sha256.Sum256(data)
	meta = blobSidecar{Key: key, ETag: `"` + hex.EncodeToString(sum[:16]) + `"`, Metadata: meta.Metadata, Size: int64(len(data)), Modified: time.Now()}
	sidecar, _ := json.Marshal(meta)
	dir := netlify.BlobsDir + "/" + blobHash(storeName)
	err = m.docker.RunOnceIn(ctx, image, m.blobsMount(p, false), bytes.NewReader(data), io.Discard,
		"sh", "-c", writeScript, "sh", dir, base, storeName, string(sidecar))
	if err != nil {
		return nil, err
	}
	return &Blob{Key: key, ETag: meta.ETag, Size: meta.Size, Modified: meta.Modified, Metadata: decodeBlobMetadata(meta.Metadata)}, nil
}

// IsNoBlobs reports whether err means the project has no blobs to browse.
func IsNoBlobs(err error) bool {
	var no errNoBlobs
	return errors.As(err, &no)
}
