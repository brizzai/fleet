package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	docs := filepath.Join(home, "Documents")

	tests := []struct {
		name     string
		path     string
		wantRoot string
		wantOK   bool
	}{
		{"project under Documents", filepath.Join(docs, "work", "dog-scratch"), docs, true},
		{"the root itself", docs, docs, true},
		{"Desktop child", filepath.Join(home, "Desktop", "x"), filepath.Join(home, "Desktop"), true},
		{"Downloads child", filepath.Join(home, "Downloads", "y"), filepath.Join(home, "Downloads"), true},
		{"unprotected code dir", filepath.Join(home, "code", "fleet"), "", false},
		{"prefix lookalike not matched", filepath.Join(home, "Documents-old"), "", false},
		{"empty path", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ok := protectedRoot(tt.path)
			if ok != tt.wantOK || root != tt.wantRoot {
				t.Errorf("protectedRoot(%q) = (%q, %v), want (%q, %v)", tt.path, root, ok, tt.wantRoot, tt.wantOK)
			}
		})
	}
}

func TestTildeHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if got := tildeHome(filepath.Join(home, "Documents")); got != "~/Documents" {
		t.Errorf("tildeHome(home/Documents) = %q, want ~/Documents", got)
	}
	if got := tildeHome("/tmp/elsewhere"); got != "/tmp/elsewhere" {
		t.Errorf("tildeHome(/tmp/elsewhere) = %q, want unchanged", got)
	}
}

func TestAnyTCCBlocked(t *testing.T) {
	h := &Home{tccBlockedRoots: map[string]bool{}}
	if h.anyTCCBlocked() {
		t.Fatal("empty map should not report blocked")
	}
	h.tccBlockedRoots["/a"] = false
	if h.anyTCCBlocked() {
		t.Fatal("all-false map should not report blocked")
	}
	h.tccBlockedRoots["/b"] = true
	if !h.anyTCCBlocked() {
		t.Fatal("a true entry should report blocked")
	}
	if got := h.firstTCCBlockedRoot(); got != "/b" {
		t.Errorf("firstTCCBlockedRoot = %q, want /b", got)
	}
}
