package plan

import (
	"strings"
	"testing"
)

const garmRecord = `# garm's record
base: gawsprivate.gigantic.io
codename: garm
customer: giantswarm
managementCluster:
  insecureCA: true
  private: true
services:
  dex:
    protocol: https # the login
`

const lineSelection = `# The agent-platform meta chart line, selected by the enable.
agentPlatform:
  kagentApiV2: true
`

// A fresh enable's record fragment lands in the record as one more key with
// its comment; every other key, comment and order stays, and the edit is
// idempotent.
func TestKeepRecordEditsTheLineInAndKeepsTheRest(t *testing.T) {
	got, kept, err := keepRecord([]byte(lineSelection), []byte(garmRecord))
	if err != nil {
		t.Fatal(err)
	}
	if kept != nil {
		t.Errorf("kept %v: the record is its own, nothing of it is the platform's to name", kept)
	}
	want := garmRecord + "# The agent-platform meta chart line, selected by the enable.\nagentPlatform:\n  kagentApiV2: true\n"
	if string(got) != want {
		t.Errorf("edited record:\n%s\nwant:\n%s", got, want)
	}
	again, _, err := keepRecord([]byte(lineSelection), got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(got) {
		t.Errorf("a second edit changed the record:\n%s", again)
	}
}

// A record that already carries the agentPlatform mapping keeps its other
// keys; a key with another value is replaced; an empty record takes the keys.
func TestKeepRecordMergesIntoAnExistingMapping(t *testing.T) {
	current := "codename: garm\nagentPlatform:\n  # the platform's block\n  fluxServiceAccountName: kagent-flux\n  kagentApiV2: false\n"
	got, _, err := keepRecord([]byte(lineSelection), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"codename: garm\n", "# the platform's block\n", "fluxServiceAccountName: kagent-flux\n", "kagentApiV2: true\n"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("edited record lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(string(got), "kagentApiV2: false") || strings.Count(string(got), "agentPlatform:") != 1 {
		t.Errorf("edited record:\n%s", got)
	}
	empty, _, err := keepRecord([]byte(lineSelection), []byte("# nothing yet\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), "agentPlatform:\n  kagentApiV2: true\n") {
		t.Errorf("an empty record takes the key:\n%s", empty)
	}
	if _, _, err := keepRecord([]byte(lineSelection), []byte("- a list\n")); err == nil {
		t.Error("a record that is no mapping takes no key")
	}
}

// The record is a shared file of the plan, edited and never created.
func TestRecordIsSharedAndTheirs(t *testing.T) {
	s := sharedFile("installations/garm/config.yaml.patch")
	if s == nil || !s.theirs {
		t.Fatalf("the record is another owner's file, edited and never created: %+v", s)
	}
	if sharedFile("installations/garm/config.yaml") != nil || sharedFile("management-clusters/garm/extras/agent-platform/secrets/muster-oauth-credentials.enc.yaml") != nil {
		t.Error("only the record at installations/<name>/config.yaml.patch is the record; the platform's own files are written whole")
	}
}
