package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExecActivityReads: a command run in a container reads as a sentence
// in the activity, with its exit code or why it ended.
func TestExecActivityReads(t *testing.T) {
	for _, test := range []struct {
		details map[string]string
		want    string
	}{
		{map[string]string{"service": "db", "command": "psql -c 'select 1'", "exit_code": "0"}, "ran `psql -c 'select 1'` in db of shop, exit 0"},
		{map[string]string{"service": "web", "command": "sleep 30", "result": "timed out after 5s"}, "ran `sleep 30` in web of shop, timed out after 5s"},
	} {
		metadata, _ := json.Marshal(test.details)
		event := ActivityEvent{Action: "app.exec", TargetID: "shop", Metadata: string(metadata)}
		if got := event.Summary(); !strings.Contains(got, test.want) {
			t.Errorf("got %q, want %q", got, test.want)
		}
	}
}
