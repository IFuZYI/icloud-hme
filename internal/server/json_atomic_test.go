package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteJSONAtomicCreatesParentAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "x.json")
	if err := writeJSONAtomic(path, map[string]string{"a": "1"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm=%v, want 0600", perm)
	}
	if err := writeJSONAtomic(path, map[string]string{"a": "2"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"2"`) {
		t.Fatalf("content=%s", raw)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("临时文件未清理")
	}
}
