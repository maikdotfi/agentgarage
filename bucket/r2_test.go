package bucket_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maikdotfi/agentgarage/bucket"
)

// s3Stub is just enough of S3 to see what the R2 store sends: objects with
// metadata, conditional PUTs and 404s.
type s3Stub struct {
	mu      sync.Mutex
	objects map[string]stubObject
	n       int
}

type stubObject struct {
	body []byte
	meta http.Header
	etag string
}

func (s *s3Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := r.URL.Path
	obj, exists := s.objects[key]
	switch r.Method {
	case http.MethodGet:
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `<Error><Code>NoSuchKey</Code></Error>`)
			return
		}
		for k, v := range obj.meta {
			w.Header()[k] = v
		}
		w.Header().Set("ETag", obj.etag)
		w.Write(obj.body)
	case http.MethodPut:
		if (r.Header.Get("If-None-Match") == "*" && exists) ||
			(r.Header.Get("If-Match") != "" && (!exists || r.Header.Get("If-Match") != obj.etag)) {
			w.WriteHeader(http.StatusPreconditionFailed)
			io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		meta := http.Header{}
		for k, v := range r.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-amz-meta-") {
				meta[k] = v
			}
		}
		s.n++
		etag := `"e` + string(rune('0'+s.n)) + `"`
		s.objects[key] = stubObject{body: body, meta: meta, etag: etag}
		w.Header().Set("ETag", etag)
	}
}

func TestR2AgainstStub(t *testing.T) {
	srv := httptest.NewServer(&s3Stub{objects: map[string]stubObject{}})
	defer srv.Close()
	store := bucket.R2(bucket.R2Config{
		Endpoint: srv.URL, Bucket: "garage", AccessKeyID: "id", SecretAccessKey: "secret",
	})
	testStore(t, store)
}

func TestR2(t *testing.T) {
	cfg := bucket.R2Config{
		Endpoint:        os.Getenv("GARAGE_TEST_R2_ENDPOINT"),
		Bucket:          os.Getenv("GARAGE_TEST_R2_BUCKET"),
		AccessKeyID:     os.Getenv("GARAGE_TEST_R2_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("GARAGE_TEST_R2_SECRET_ACCESS_KEY"),
	}
	if cfg.Endpoint == "" {
		t.Skip("GARAGE_TEST_R2_ENDPOINT not set")
	}
	testStore(t, bucket.R2(cfg))
}

func TestFakeStore(t *testing.T) {
	testStore(t, bucket.NewFake())
}

// testStore is what every Store must do, whichever bucket is behind it.
func testStore(t *testing.T, store bucket.Store) {
	ctx := context.Background()
	key := "test/" + time.Now().Format("20060102T150405.000000000")

	if _, err := store.Get(ctx, key); !errors.Is(err, bucket.ErrNotFound) {
		t.Fatalf("get missing: err = %v, want ErrNotFound", err)
	}
	etag, err := store.Put(ctx, key, bucket.Object{
		Body: []byte("one"), Meta: map[string]string{"garage-sig": "c2ln"},
	}, bucket.Cond{IfNoneMatch: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	obj, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(obj.Body) != "one" || obj.Meta["garage-sig"] != "c2ln" || obj.ETag != etag {
		t.Errorf("got body %q meta %v etag %q, want one, the signature, %q", obj.Body, obj.Meta, obj.ETag, etag)
	}

	_, err = store.Put(ctx, key, bucket.Object{Body: []byte("two")}, bucket.Cond{IfNoneMatch: true})
	if !errors.Is(err, bucket.ErrPrecondition) {
		t.Errorf("create existing: err = %v, want ErrPrecondition", err)
	}
	if _, err := store.Put(ctx, key, bucket.Object{Body: []byte("two")}, bucket.Cond{IfMatch: etag}); err != nil {
		t.Errorf("swap at current etag: %v", err)
	}
	_, err = store.Put(ctx, key, bucket.Object{Body: []byte("three")}, bucket.Cond{IfMatch: etag})
	if !errors.Is(err, bucket.ErrPrecondition) {
		t.Errorf("swap at stale etag: err = %v, want ErrPrecondition", err)
	}
}
