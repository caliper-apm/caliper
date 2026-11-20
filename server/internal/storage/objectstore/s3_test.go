package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestS3StoreSigV4PutGetDelete(t *testing.T) {
	var (
		mu      sync.Mutex
		objects = map[string][]byte{}
		sawSig  bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=ak-test/") &&
			r.Header.Get("X-Amz-Content-Sha256") != "" &&
			r.Header.Get("X-Amz-Date") != "" {
			sawSig = true
		}
		switch r.Method {
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			b, ok := objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write(b)
		case http.MethodDelete:
			delete(objects, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	store, err := NewS3(S3Config{
		Endpoint:  srv.URL,
		Region:    "us-east-1",
		Bucket:    "caliper-symbols",
		AccessKey: "ak-test",
		SecretKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	ctx := context.Background()
	payload := []byte("com.example.App -> a.a:\n    void run() -> a\n")
	if err := store.PutObject(ctx, "android/1.1.0/r8.txt", payload, "text/plain"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, ok, err := store.GetObject(ctx, "android/1.1.0/r8.txt")
	if err != nil || !ok || string(got) != string(payload) {
		t.Fatalf("get mismatch: ok=%v err=%v got=%q", ok, err, string(got))
	}
	if err := store.DeleteObject(ctx, "android/1.1.0/r8.txt"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, ok, err = store.GetObject(ctx, "android/1.1.0/r8.txt")
	if err != nil || ok {
		t.Fatalf("expected not found after delete, got ok=%v err=%v", ok, err)
	}
	mu.Lock()
	if !sawSig {
		t.Fatalf("expected AWS SigV4 Authorization & X-Amz-Content-Sha256 headers")
	}
	mu.Unlock()
}
