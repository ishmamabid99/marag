package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Real repos carry junk (venv/node_modules) and broken symlinks that must not
// abort ingestion (regression for the venv/bin/python failure).
func TestReadDirSkipsJunkAndBrokenSymlinks(t *testing.T) {
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// real source
	must(os.WriteFile(filepath.Join(root, "main.go"), []byte("package x\n"), 0o644))
	// vendor dir (should be skipped)
	must(os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755))
	must(os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("junk\n"), 0o644))
	// venv with a broken symlink (the exact failure we hit)
	must(os.MkdirAll(filepath.Join(root, "venv", "bin"), 0o755))
	must(os.Symlink("/nonexistent/python", filepath.Join(root, "venv", "bin", "python")))

	files, err := readDir(root)
	if err != nil {
		t.Fatalf("readDir must tolerate junk/broken symlinks, got: %v", err)
	}
	if _, ok := files["main.go"]; !ok {
		t.Fatalf("main.go should be ingested; got %v", keys(files))
	}
	for p := range files {
		if p != "main.go" {
			t.Fatalf("only main.go expected, got extra: %s", p)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
