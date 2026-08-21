package migrations

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestFilesGlobUnsafePath proves Files finds migrations when the checkout path
// contains glob metacharacters — filepath.Glob on such a path silently matched
// nothing (ARG-548). It also locks in the up/down ordering contract.
func TestFilesGlobUnsafePath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "work[dir] ?")
	dir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	for _, name := range []string{
		"0002_second.up.sql",
		"0001_first.up.sql",
		"0001_first.down.sql",
		"0002_second.down.sql",
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("-- test"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Chdir(root)

	up, err := Files("up")
	if err != nil {
		t.Fatalf("Files(up): %v", err)
	}
	wantUp := []string{
		filepath.Join("migrations", "0001_first.up.sql"),
		filepath.Join("migrations", "0002_second.up.sql"),
	}
	if !reflect.DeepEqual(up, wantUp) {
		t.Errorf("Files(up) = %v, want %v", up, wantUp)
	}

	down, err := Files("down")
	if err != nil {
		t.Fatalf("Files(down): %v", err)
	}
	wantDown := []string{
		filepath.Join("migrations", "0002_second.down.sql"),
		filepath.Join("migrations", "0001_first.down.sql"),
	}
	if !reflect.DeepEqual(down, wantDown) {
		t.Errorf("Files(down) = %v, want %v", down, wantDown)
	}
}
