package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// lister answers warnUnmigrated as the store would.
type lister struct {
	slugs []string
	err   error
}

func (l lister) UnmigratedCredentials(context.Context) ([]string, error) { return l.slugs, l.err }

// logged runs warnUnmigrated and returns the lines it wrote.
func logged(t *testing.T, l lister) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	warnUnmigrated(context.Background(), l, slog.New(slog.NewJSONHandler(&buf, nil)))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("%v in %q", err, line)
		}
		out = append(out, rec)
	}
	return out
}

// At start the gateway names, once and as an error, the servers whose
// credential is still in its old store, with the command that moves them.
func TestStartNamesTheServersNotMovedToTheKeyring(t *testing.T) {
	lines := logged(t, lister{slugs: []string{"atlas", "github"}})
	if len(lines) != 1 {
		t.Fatalf("%d log lines, want one: %v", len(lines), lines)
	}
	line := lines[0]
	msg, _ := line["msg"].(string)
	if line["level"] != "ERROR" || !strings.Contains(msg, "gateway migrate-credentials") {
		t.Fatalf("not an error that names the command: %v", line)
	}
	servers, _ := line["servers"].([]any)
	if len(servers) != 2 || servers[0] != "atlas" || servers[1] != "github" || line["count"] != float64(2) {
		t.Fatalf("the servers are not named: %v", line)
	}
}

// With nothing left to move it says nothing.
func TestStartSaysNothingWhenEveryCredentialIsInTheKeyring(t *testing.T) {
	for name, l := range map[string]lister{"nil": {}, "empty": {slugs: []string{}}} {
		if lines := logged(t, l); len(lines) != 0 {
			t.Fatalf("%s: logged %v", name, lines)
		}
	}
}

// A store that cannot be asked is reported, and does not stop the start.
func TestStartReportsThatItCouldNotCheck(t *testing.T) {
	lines := logged(t, lister{err: errors.New("the database is away")})
	if len(lines) != 1 || lines[0]["level"] != "ERROR" || lines[0]["err"] != "the database is away" {
		t.Fatalf("logged %v", lines)
	}
}
