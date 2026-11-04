package credentials

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRestrictedFile(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
		valid      bool
	}{
		{"owner read", "credential", 0400, true},
		{"owner write", "credential", 0600, true},
		{"group read", "credential", 0640, false},
		{"other read", "credential", 0604, false},
		{"no owner read", "credential", 0200, false},
		{"too large", strings.Repeat("a", 65537), 0600, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
				t.Fatal(err)
			}
			data, err := ReadRestrictedFile(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, err)
			}
			if tc.valid && string(data) != tc.body {
				t.Fatal("file contents differ")
			}
		})
	}
	dir := t.TempDir()
	for _, path := range []string{dir, filepath.Join(dir, "absent")} {
		if _, err := ReadRestrictedFile(path); err == nil {
			t.Fatal("invalid path accepted")
		}
	}
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRestrictedFile(link); err == nil {
		t.Fatal("symlink accepted")
	}
}
