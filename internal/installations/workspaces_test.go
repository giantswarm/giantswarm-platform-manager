package installations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// The workspaces on record are the installation's agent-platform values:
// on with the base URL and the client the values name, nil where the patch
// does not exist, carries no switch or turns it off, or the installation
// has no configs repository; an unreadable patch is an error naming it.
func TestReadWorkspaces(t *testing.T) {
	const configs, baseURL = "fleet/umbra-configs", "https://ws.maple.example"
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{Configs: configs}}
	path := AgentPlatformPatchPath(fixtureInstallation)
	broken := errors.New("the token expired")
	cases := []struct {
		name    string
		inst    Installation
		patch   string
		readErr error
		want    *Workspaces
		wantErr string
	}{
		{"no configs repository", Installation{Name: fixtureInstallation}, "", nil, nil, ""},
		{"no patch on record", inst, "", gh.ErrNotFound, nil, ""},
		{"a patch without the switch", inst, "global:\n  domain: maple.example\n", nil, nil, ""},
		{"the switch off", inst, "workspaces:\n  enabled: false\nworkspace-manager:\n  oauth:\n    baseURL: " + baseURL + "\n", nil, nil, ""},
		{"the switch on", inst, "workspaces:\n  enabled: true\n  storage:\n    storageClassName: nfs\n", nil, &Workspaces{Enabled: true}, ""},
		{"the switch on with a base URL and a client", inst, "workspaces:\n  enabled: true\nworkspace-manager:\n  oauth:\n    baseURL: " + baseURL + "\n    dex:\n      clientID: ws-client\n", nil, &Workspaces{Enabled: true, BaseURL: baseURL, ClientID: "ws-client"}, ""},
		{"a patch that is no YAML", inst, "workspaces: [\n", nil, nil, "the workspaces on record: " + path + ": "},
		{"a read that fails", inst, "", broken, nil, "the workspaces on record: " + broken.Error()},
	}
	for _, c := range cases {
		read := func(_ context.Context, repository, p string) (string, error) {
			if repository != configs || p != path {
				t.Errorf("%s: read %s:%s, want %s:%s", c.name, repository, p, configs, path)
			}
			return c.patch, c.readErr
		}
		got, err := readWorkspaces(t.Context(), read, c.inst)
		if c.wantErr != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.wantErr) {
				t.Errorf("%s: error %v, want one starting %q", c.name, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		switch {
		case got == nil && c.want == nil:
		case got == nil || c.want == nil || *got != *c.want:
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	on := Workspaces{Enabled: true, BaseURL: baseURL}
	if in := on.Input(); in[keyEnabled] != true || in["baseUrl"] != on.BaseURL || len(in) != 2 {
		t.Errorf("Input: %v", in)
	}
}

// The agent-platform definition's record inputs carry installation.workspaces
// where the capability is enabled and the values on record turn workspaces
// on, read as the caller; an installation without the capability reads
// nothing, its marker never read by content.
func TestPlatformRecordInputs(t *testing.T) {
	const configs = "fleet/umbra-configs"
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{Configs: configs}}
	platform, _ := FindCapability(AgentPlatform)
	enabled := func(on bool) Report {
		r := Report{Installation: inst}
		r.Capabilities = []CapabilityState{{Name: AgentPlatform, Enabled: on}}
		return r
	}
	const baseURL = "https://ws.maple.example"
	reads := 0
	read := func(_ context.Context, _, _ string) (string, error) {
		reads++
		return "workspaces:\n  enabled: true\nworkspace-manager:\n  oauth:\n    baseURL: " + baseURL + "\n", nil
	}
	got, err := platform.RecordInputs(t.Context(), enabled(true), read)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := got[InputsInstallation].(map[string]any)["workspaces"].(map[string]any)
	if ws[keyEnabled] != true || ws["baseUrl"] != baseURL || reads != 1 {
		t.Errorf("enabled: %v after %d reads", got, reads)
	}
	if got, err := platform.RecordInputs(t.Context(), enabled(false), read); err != nil || got != nil || reads != 1 {
		t.Errorf("not enabled: %v, %v after %d reads", got, err, reads)
	}
}
