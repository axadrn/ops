package service

import (
	"context"
	"errors"
	"testing"
)

// TestBadNamesStopBeforeTheServer: a restore without a moment or a
// made-up ref name is refused before moin looks anything up, sends it to
// GitHub or to an agent.
func TestBadNamesStopBeforeTheServer(t *testing.T) {
	s := &AppService{}
	ctx := context.Background()
	if _, err := s.StartRestore(ctx, Actor{}, "blog", RestoreTarget{Database: "app.db", Moment: "  "}); !errors.Is(err, ErrRestoreTarget) {
		t.Errorf("Restore: err = %v, want ErrRestoreTarget", err)
	}
	if _, err := s.DeployRef(ctx, Actor{}, "blog", "--upload-pack=x"); !errors.Is(err, ErrRefName) {
		t.Errorf("DeployRef: err = %v, want ErrRefName", err)
	}
	if _, err := s.LinkRepo(ctx, Actor{}, "blog", "acme/blog", "main..evil", true); !errors.Is(err, ErrRefName) {
		t.Errorf("LinkRepo: err = %v, want ErrRefName", err)
	}
}
