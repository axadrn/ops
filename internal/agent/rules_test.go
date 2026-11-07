package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/axadrn/moin/protocol"
)

func TestRulesReadEveryRule(t *testing.T) {
	rules, err := parseRules(`
# what moin may do here
data: off
logs: off   # no logs through moin
network: locked
deploy-from: github.com/acme/*, ghcr.io/acme/*, upload
backups-to: s3.eu-central-1.amazonaws.com/acme-backups
`, "rules")
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.Rules{DataOff: true, LogsOff: true, NetworkLocked: true, DeployFrom: []string{"github.com/acme/*", "ghcr.io/acme/*", "upload"}, BackupsTo: "s3.eu-central-1.amazonaws.com/acme-backups"}
	if strings.Join(rules.DeployFrom, ",") != strings.Join(want.DeployFrom, ",") || rules.DataOff != want.DataOff || rules.LogsOff != want.LogsOff || rules.NetworkLocked != want.NetworkLocked || rules.BackupsTo != want.BackupsTo {
		t.Fatalf("rules = %+v", rules)
	}
}

func TestRulesWithAMistakeAreAnError(t *testing.T) {
	for _, text := range []string{"dta: off", "data off", "data: no", "network: closed", "backups-to: acme-backups", "deploy-from: github.com/[acme"} {
		if _, err := parseRules(text, "rules"); err == nil {
			t.Errorf("%q: no error", text)
		}
	}
}

func TestNoRulesFileAllowsEverything(t *testing.T) {
	rules, err := LoadRules(filepath.Join(t.TempDir(), "rules"))
	if err != nil || rules != nil {
		t.Fatalf("rules = %+v, %v", rules, err)
	}
	if checkDeploy(rules, "rules", protocol.DeployStart{Image: "evil.example/x:1"}) != nil {
		t.Fatal("a deploy without rules is refused")
	}
}

func TestDeployFrom(t *testing.T) {
	rules := &protocol.Rules{DeployFrom: []string{"github.com/acme/*", "ghcr.io/acme/*"}}
	cases := []struct {
		start protocol.DeployStart
		ok    bool
	}{
		{protocol.DeployStart{SourceURL: "https://api.github.com/repos/acme/blog/tarball/abc123"}, true},
		{protocol.DeployStart{SourceURL: "https://api.github.com/repos/evil/blog/tarball/abc123"}, false},
		{protocol.DeployStart{SourceURL: "https://evil.example/repos/acme/blog/tarball/abc123"}, false},
		{protocol.DeployStart{SourceURL: "https://bucket.example/uploads/x.tar.gz"}, false},
		{protocol.DeployStart{Image: "ghcr.io/acme/blog:v3"}, true},
		{protocol.DeployStart{Image: "ghcr.io/acme/blog@sha256:abc"}, true},
		{protocol.DeployStart{Image: "ghcr.io/evil/blog:v3"}, false},
		{protocol.DeployStart{SourceNumber: 4}, true},
		{protocol.DeployStart{FromNumber: 4}, true},
	}
	for _, c := range cases {
		err := checkDeploy(rules, "rules", c.start)
		if (err == nil) != c.ok {
			t.Errorf("%+v: err = %v", c.start, err)
		}
	}
}

func TestUploadsComeFromTheBucketOfBackupsTo(t *testing.T) {
	rules := &protocol.Rules{DeployFrom: []string{"upload"}, BackupsTo: "s3.example.com/acme"}
	for raw, ok := range map[string]bool{
		"https://s3.example.com/acme/uploads/x.tar.gz?X-Amz-Signature=1": true,
		"https://acme.s3.example.com/uploads/x.tar.gz":                   true,
		"https://s3.example.com/other/uploads/x.tar.gz":                  false,
		"https://evil.example/acme/uploads/x.tar.gz":                     false,
	} {
		err := checkDeploy(rules, "rules", protocol.DeployStart{SourceURL: raw})
		if (err == nil) != ok {
			t.Errorf("%s: err = %v", raw, err)
		}
	}
}

func TestBackupsTo(t *testing.T) {
	rules := &protocol.Rules{BackupsTo: "s3.eu-central-1.amazonaws.com/acme"}
	if checkBucket(rules, "rules", &protocol.Bucket{Region: "eu-central-1", Bucket: "acme"}) != nil {
		t.Fatal("the allowed AWS bucket is refused")
	}
	if checkBucket(rules, "rules", &protocol.Bucket{Endpoint: "https://evil.example", Bucket: "acme"}) == nil {
		t.Fatal("another bucket is allowed")
	}
	if checkBucket(rules, "rules", nil) != nil {
		t.Fatal("turning backups off is refused")
	}
}

func TestHelloCarriesTheRules(t *testing.T) {
	file := filepath.Join(t.TempDir(), "rules")
	os.WriteFile(file, []byte("data: off\n"), 0o600)
	t.Setenv("MOIN_RULES", file)
	if hello := Hello(); hello.Rules == nil || !hello.Rules.DataOff {
		t.Fatalf("hello = %+v", hello)
	}
}

// OPS_RULES of the old name counts for nothing: only MOIN_RULES, else
// /etc/moin/rules.
func TestRulesFileOldNameIgnored(t *testing.T) {
	t.Setenv("MOIN_RULES", "")
	t.Setenv("OPS_RULES", filepath.Join(t.TempDir(), "rules"))
	if got := RulesFile(); got != "/etc/moin/rules" {
		t.Fatalf("RulesFile = %q", got)
	}
}
