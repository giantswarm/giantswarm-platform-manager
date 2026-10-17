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
}

// The workspaces on record are reported, never an input: the agent-platform
// definition derives nothing from the record beyond the registry's facts.
func TestPlatformRecordInputs(t *testing.T) {
	if platform, _ := FindCapability(AgentPlatform); platform.RecordInputs != nil {
		t.Error("the agent-platform definition derives inputs from the record")
	}
}

// A report's workspaces are the agent-platform values on record where the
// platform is on record: on with what the values name, off where they leave
// the switch off; off without a read where the platform is not on record;
// unknown, with the error on the report, where the values cannot be read.
func TestReportReadWorkspaces(t *testing.T) {
	const configs, baseURL = "fleet/umbra-configs", "https://ws.maple.example"
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{Configs: configs}}
	broken := errors.New("the token expired")
	cases := []struct {
		name      string
		platform  bool
		patch     string
		readErr   error
		want      *Workspaces
		wantReads int
	}{
		{"the platform not on record", false, "", nil, &Workspaces{}, 0},
		{"the switch on", true, "workspaces:\n  enabled: true\nworkspace-manager:\n  oauth:\n    baseURL: " + baseURL + "\n", nil, &Workspaces{Enabled: true, BaseURL: baseURL}, 1},
		{"no switch", true, "muster: {}\n", nil, &Workspaces{}, 1},
		{"the switch off", true, "workspaces:\n  enabled: false\n", nil, &Workspaces{}, 1},
		{"a read that fails", true, "", broken, nil, 1},
	}
	for _, c := range cases {
		reads := 0
		read := func(_ context.Context, _, _ string) (string, error) {
			reads++
			return c.patch, c.readErr
		}
		r := Report{Installation: inst, Capabilities: []CapabilityState{{Name: AgentPlatform, Enabled: c.platform}}}
		r.readWorkspaces(t.Context(), read)
		switch {
		case reads != c.wantReads:
			t.Errorf("%s: %d reads, want %d", c.name, reads, c.wantReads)
		case (r.Workspaces == nil) != (c.want == nil) || r.Workspaces != nil && *r.Workspaces != *c.want:
			t.Errorf("%s: %+v, want %+v", c.name, r.Workspaces, c.want)
		case (c.readErr != nil) != (len(r.Errors) == 1):
			t.Errorf("%s: errors %v", c.name, r.Errors)
		}
	}
}
