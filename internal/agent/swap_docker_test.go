//go:build docker

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPgSwapScript runs the swap in the real postgres:17 image, under its
// /bin/sh (dash): the restored data goes in, hidden files too; a copy
// that fails and a move aside that fails both leave the live data as it
// was; a .moin-old an earlier swap left is never touched; a cleanup that
// fails after a finished swap is no failure.
func TestPgSwapScript(t *testing.T) {
	ctx := context.Background()
	write := func(dir string, files map[string]string) {
		for name, body := range files {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		}
	}
	read := func(dir, name string) string {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "missing"
		}
		return string(body)
	}
	swap := func(live, restore string, extra ...string) (int, string) {
		args := []string{"run", "--rm", "-v", live + ":/data"}
		if restore != "" {
			args = append(args, "-v", restore+":/restore:ro")
		}
		args = append(args, extra...)
		args = append(args, "--entrypoint", "sh", "postgres:17", "-c", pgSwapScript, "/data", "restore-1")
		code, out, _ := exitCode(ctx, "docker", args...)
		return code, out
	}
	// root owns what the container moved; the test's cleanup must remove it.
	cleanup := func(dirs ...string) {
		for _, dir := range dirs {
			exec.Command("docker", "run", "--rm", "-v", dir+":/x", "alpine:latest", "sh", "-c", "rm -rf /x/* /x/.[!.]*").Run()
		}
	}
	oldData := map[string]string{"PG_VERSION": "old", "base/1": "old rows", ".hidden": "old"}
	newData := map[string]string{"PG_VERSION": "new", "base/1": "new rows", ".restored": "new", "recovery.signal": ""}

	// A copy that fails: nothing changed.
	live, restored := t.TempDir(), t.TempDir()
	defer cleanup(live, restored)
	write(live, oldData)
	write(restored, newData)
	if code, out := swap(live, ""); code != swapUnchanged || !strings.Contains(out, "nothing changed") || read(live, "base/1") != "old rows" || read(live, ".hidden") != "old" {
		t.Fatalf("a failed copy: %d %s", code, out)
	}
	if read(live, ".moin-new/PG_VERSION") != "missing" || read(live, ".moin-old/PG_VERSION") != "missing" {
		t.Fatal("a failed copy left its folders")
	}

	// Moving aside fails at a mount point, which cannot move: everything
	// moves back, nothing changed.
	mounted := t.TempDir()
	defer cleanup(mounted)
	os.MkdirAll(filepath.Join(live, "zz-mount"), 0o755)
	code, out := swap(live, restored, "-v", mounted+":/data/zz-mount")
	if code != swapUnchanged || !strings.Contains(out, "nothing changed") || read(live, "base/1") != "old rows" || read(live, "PG_VERSION") != "old" || read(live, ".hidden") != "old" {
		t.Fatalf("a failed move aside: %d %s; base/1 %q", code, out, read(live, "base/1"))
	}
	entries, _ := os.ReadDir(live)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".moin-") {
			t.Errorf("a failed move aside left %s", entry.Name())
		}
	}
	os.Remove(filepath.Join(live, "zz-mount"))

	// A swap that works.
	if code, out := swap(live, restored); code != 0 {
		t.Fatalf("swap: %d %s", code, out)
	}
	if read(live, "PG_VERSION") != "new" || read(live, "base/1") != "new rows" || read(live, ".restored") != "new" || read(live, ".moin-restored") != "restore-1" {
		t.Errorf("the new data is not in place: %q %q %q %q", read(live, "PG_VERSION"), read(live, "base/1"), read(live, ".restored"), read(live, ".moin-restored"))
	}
	if read(live, ".hidden") != "missing" || read(live, "recovery.signal") != "missing" || read(live, ".moin-old/PG_VERSION") != "missing" {
		t.Error("old files or the recovery signal are left")
	}

	// An earlier swap stopped halfway and left .moin-old: the next one
	// refuses and touches nothing.
	write(live, map[string]string{".moin-old/PG_VERSION": "kept"})
	if code, out := swap(live, restored); code != swapRefused || read(live, ".moin-old/PG_VERSION") != "kept" || read(live, "PG_VERSION") != "new" {
		t.Fatalf("a swap over a .moin-old: %d %s", code, out)
	}
	cleanup(live)

	// The cleanup after a finished swap fails, here at a mount point in the
	// data before: the swap counts, what is left moves aside.
	write(live, map[string]string{"PG_VERSION": "old"})
	os.MkdirAll(filepath.Join(live, "base", "inner"), 0o755)
	code, out = swap(live, restored, "-v", mounted+":/data/base/inner")
	if code != 0 || read(live, "PG_VERSION") != "new" {
		t.Fatalf("a failed cleanup: %d %s, PG_VERSION %q", code, out, read(live, "PG_VERSION"))
	}
	if _, err := os.Stat(filepath.Join(live, ".moin-old")); err == nil {
		t.Error("a failed cleanup left .moin-old, which blocks the next swap")
	}
}
