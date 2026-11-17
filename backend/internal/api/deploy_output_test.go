package api

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"git.zefirlabs.net/zefir/nudge/backend/internal/stacks"
)

func TestComposeTail(t *testing.T) {
	t.Parallel()
	var tail composeTail
	for i := range composeTailLines + 5 {
		tail.add(stacks.Line{Text: "line " + strconv.Itoa(i)})
	}
	tail.add(stacks.Line{Text: "   "})
	got := tail.redacted(nil)
	if len(got) != composeTailLines || got[0] != "line 5" || got[len(got)-1] != "line 24" {
		t.Errorf("kept %q, want the last %d lines", got, composeTailLines)
	}

	tail = composeTail{}
	long := strings.Repeat("é", composeLineBytes)
	for range 20 {
		tail.add(stacks.Line{Text: long})
	}
	got = tail.redacted(nil)
	size := 0
	for _, l := range got {
		size += len(l)
		if len(l) > composeLineBytes+len("…") || !strings.HasSuffix(l, "…") {
			t.Errorf("long line kept as %d bytes", len(l))
		}
	}
	if size > composeTailBytes || len(got) == 0 {
		t.Errorf("kept %d lines, %d bytes; want at most %d bytes", len(got), size, composeTailBytes)
	}
}

func TestEnvSecrets(t *testing.T) {
	t.Parallel()
	before := "# c\nAPI_IMAGE=app:v1\nDB_PASSWORD='hunter22'\nKEEP=1\nEMPTY=\n"
	after := "API_IMAGE=app:v2\nDB_PASSWORD='hunter22'\nKEEP=1\nTOKEN=app:v2\nOTHER=s3cret!\n"
	got := envSecrets(map[string]string{"API_IMAGE": "app:v2"}, before, after)
	slices.Sort(got)
	// app:v2 is the run's own value, but TOKEN holds it too; KEEP's is too short.
	want := []string{"app:v1", "app:v2", "hunter22", "s3cret!"}
	if !slices.Equal(got, want) {
		t.Errorf("envSecrets = %q, want %q", got, want)
	}
	if got := envSecrets(map[string]string{"API_IMAGE": "app:v2"}, "API_IMAGE=app:v2\n"); len(got) != 0 {
		t.Errorf("envSecrets of the run's own value = %q, want none", got)
	}

	var tail composeTail
	tail.add(stacks.Line{Text: "connect: password hunter22 refused"})
	tail.add(stacks.Line{Text: "pull app:v3"})
	if got := tail.redacted(want); got[0] != hiddenComposeLine || got[1] != "pull app:v3" {
		t.Errorf("redacted = %q", got)
	}
}
