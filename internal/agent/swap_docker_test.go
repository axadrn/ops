//go:build docker

package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPgSwapScript: the swap puts the restored data in place, hidden
// files too, and a copy that fails leaves the live data as it was and
// says nothing changed.
func TestPgSwapScript(t *testing.T) {
	live, restored := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"PG_VERSION": "old", "base/1": "old rows", ".hidden": "old"} {
		os.MkdirAll(filepath.Dir(filepath.Join(live, name)), 0o755)
		os.WriteFile(filepath.Join(live, name), []byte(body), 0o644)
	}
	for name, body := range map[string]string{"PG_VERSION": "new", "base/1": "new rows", ".restored": "new", "recovery.signal": ""} {
		os.MkdirAll(filepath.Dir(filepath.Join(restored, name)), 0o755)
		os.WriteFile(filepath.Join(restored, name), []byte(body), 0o644)
	}
	swap := func(restore string) (string, error) {
		args := []string{"run", "--rm", "-v", live + ":/data"}
		if restore != "" {
			args = append(args, "-v", restore+":/restore:ro")
		}
		out, err := exec.Command("docker", append(args, "alpine:latest", "sh", "-c", pgSwapScript, "/data")...).CombinedOutput()
		return string(out), err
	}
	read := func(name string) string {
		body, err := os.ReadFile(filepath.Join(live, name))
		if err != nil {
			return "missing"
		}
		return string(body)
	}

	// No restored data: the copy fails, nothing changed.
	out, err := swap("")
	if err == nil || !strings.Contains(out, "nothing changed") || read("base/1") != "old rows" || read(".hidden") != "old" {
		t.Fatalf("a failed copy: %v %s; base/1 %q", err, out, read("base/1"))
	}
	entries, _ := os.ReadDir(live)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".moin-") {
			t.Errorf("left %s", entry.Name())
		}
	}

	out, err = swap(restored)
	if err != nil {
		t.Fatalf("swap: %v %s", err, out)
	}
	if read("PG_VERSION") != "new" || read("base/1") != "new rows" || read(".restored") != "new" {
		t.Errorf("the new data is not in place: %q %q %q", read("PG_VERSION"), read("base/1"), read(".restored"))
	}
	if read(".hidden") != "missing" || read("recovery.signal") != "missing" || read(".moin-old/PG_VERSION") != "missing" {
		t.Error("old files or the recovery signal are left")
	}
}
