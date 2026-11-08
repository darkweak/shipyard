package deploy

import (
	"strings"
	"testing"
)

func TestParseBlobListing(t *testing.T) {
	s := blobHash("site:cache")
	raw := strings.Join([]string{
		s + "/store", "site:cache",
		s + "/" + blobHash("b") + ".json", `{"key":"b","etag":"\"2\"","size":2,"modified":"2026-11-01T10:00:00Z"}`,
		s + "/" + blobHash("a/x") + ".json", `{"key":"a/x","etag":"\"1\"","metadata":"b64;eyJ0eXBlIjoiaW1hZ2UifQ==","size":5,"modified":"2026-11-01T09:00:00Z"}`,
		"orphan/" + blobHash("z") + ".json", `{"key":"z"}`, // no store file: skipped
		"empty/store", "deploy:abc",
		"",
	}, "\x00")
	stores := parseBlobListing([]byte(raw))
	if len(stores) != 2 || stores[0].Name != "deploy:abc" || stores[1].Name != "site:cache" {
		t.Fatalf("stores: %+v", stores)
	}
	if len(stores[0].Blobs) != 0 {
		t.Fatalf("empty store has blobs: %+v", stores[0].Blobs)
	}
	bs := stores[1].Blobs
	if len(bs) != 2 || bs[0].Key != "a/x" || bs[1].Key != "b" || bs[0].Size != 5 {
		t.Fatalf("blobs: %+v", bs)
	}
	if string(bs[0].Metadata) != `{"type":"image"}` || bs[1].Metadata != nil {
		t.Fatalf("metadata: %s / %s", bs[0].Metadata, bs[1].Metadata)
	}
}

func TestBlobPathMatchesServerLayout(t *testing.T) {
	// sha256("s") — the server stores blobs under dir/sha256(store)/sha256(key).
	const h = "043a718774c572bd8a25adbeb1bfcd5c0256ae11cecf9f9c3f925d0e52beaf89"
	if got := blobPath("s", "s"); got != "/blobs/"+h+"/"+h {
		t.Fatalf("blobPath: %s", got)
	}
}
