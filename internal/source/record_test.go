package source

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mohsensc/tend-cli/internal/core"
	"github.com/mohsensc/tend-cli/internal/fetch"
)

// Each Record test talks to the real service and rewrites testdata/recorded.
// Run them with TEND_RECORD=1 (make record).
func recorder(t *testing.T) core.Fetcher {
	t.Helper()
	if os.Getenv("TEND_RECORD") != "1" {
		t.Skip("set TEND_RECORD=1 to record")
	}
	c := fetch.New("", "record")
	c.Token = func(host string) string {
		if host == "api.github.com" {
			return os.Getenv("GITHUB_TOKEN")
		}
		return ""
	}
	return fetch.Record(c, recorded)
}

func TestRecordOSV(t *testing.T) {
	if _, err := (&OSV{Fetch: recorder(t)}).Changes(context.Background(), osvInventory()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordEOL(t *testing.T) {
	s := &EOL{Fetch: recorder(t), Now: func() time.Time { return recordedNow }}
	if _, err := s.Changes(context.Background(), eolInventory()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordArchived(t *testing.T) {
	if _, err := Archived(context.Background(), recorder(t), archivedRepos); err != nil {
		t.Fatal(err)
	}
}

func TestRecordNotes(t *testing.T) {
	f := recorder(t)
	ctx := context.Background()
	if _, err := Notes(ctx, f, releasesMeta, releasesFrom, releasesTo); err != nil {
		t.Fatal(err)
	}
	if _, err := Notes(ctx, f, changelogMeta, changelogFrom, changelogTo); err != nil {
		t.Fatal(err)
	}
}
