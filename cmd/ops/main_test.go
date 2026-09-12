package main

import (
	"context"
	"testing"
)

func TestRetryCommands(t *testing.T) {
	for _, kind := range []string{"digest", "backfill"} {
		task, id, err := parseRetryArguments([]string{"retry", kind, "--id", "123"})
		if err != nil || task != kind || id != 123 {
			t.Fatalf("%s: %s/%d/%v", kind, task, id, err)
		}
	}
	for _, args := range [][]string{{}, {"retry"}, {"retry", "other", "--id", "1"}, {"retry", "digest"}, {"retry", "backfill", "--id", "0"}, {"retry", "digest", "--id", "-1"}, {"retry", "digest", "--id", "1", "extra"}} {
		if _, _, err := parseRetryArguments(args); err == nil {
			t.Fatalf("accepted invalid command %v", args)
		}
	}
}

func TestRemovedRoleCommandsFailBeforeOpeningDependencies(t *testing.T) {
	for _, command := range []string{"grant", "revoke", "list"} {
		if err := run(context.Background(), []string{"role", command}); err == nil || err.Error() != "usage: signalwatch-ops retry <digest|backfill> --id ID" {
			t.Fatalf("%s: %v", command, err)
		}
	}
}
