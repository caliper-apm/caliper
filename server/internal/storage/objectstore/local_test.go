package objectstore

import (
	"bytes"
	"context"
	"testing"
)

func TestLocalAndMemoryObjectStores(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	mem := NewMemory()

	for name, store := range map[string]ObjectStore{"local": local, "memory": mem} {
		payload := []byte("caliper-symbol-blob-v16")
		if err := store.PutObject(ctx, "symbols/demo/android/1.6.0/160/sym.bin", payload, "application/octet-stream"); err != nil {
			t.Fatalf("%s PutObject: %v", name, err)
		}
		got, ok, err := store.GetObject(ctx, "symbols/demo/android/1.6.0/160/sym.bin")
		if err != nil || !ok || !bytes.Equal(got, payload) {
			t.Fatalf("%s GetObject: err=%v ok=%v got=%q", name, err, ok, got)
		}
		if err := store.DeleteObject(ctx, "symbols/demo/android/1.6.0/160/sym.bin"); err != nil {
			t.Fatalf("%s DeleteObject: %v", name, err)
		}
		if _, ok, err := store.GetObject(ctx, "symbols/demo/android/1.6.0/160/sym.bin"); err != nil || ok {
			t.Fatalf("%s expected deleted object absent, err=%v ok=%v", name, err, ok)
		}
	}

	// Path traversal guard on LocalStore
	if err := local.PutObject(ctx, "../../escape.bin", []byte("x"), ""); err == nil {
		t.Fatal("expected LocalStore to reject path traversal key")
	}
}
