package installations

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// Workspaces is the workspace-manager as an installation's own agent-platform
// values turn it on: workspaces.enabled, the manager's public base URL where
// the values set one (workspace-manager.oauth.baseURL) and the Dex client it
// signs the person's browser in with where they name one
// (workspace-manager.oauth.dex.clientID). The definition renders none of the
// keys and registers the manager's sign-in redirect URI from them.
type Workspaces struct {
	Enabled  bool   `json:"enabled"`
	BaseURL  string `json:"baseUrl,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// keyWorkspacesEnabled is the switch's key, in the values on record and in the input.
const keyWorkspacesEnabled = "enabled"

// Input is the fact in the shape of the definitions' installation.workspaces.
func (w Workspaces) Input() map[string]any {
	in := map[string]any{keyWorkspacesEnabled: w.Enabled}
	if w.BaseURL != "" {
		in["baseUrl"] = w.BaseURL
	}
	if w.ClientID != "" {
		in["clientId"] = w.ClientID
	}
	return in
}

// platformRecordInputs are the agent-platform definition's inputs from the
// record beyond the registry's facts: installation.workspaces, read from the
// installation's agent-platform values as the caller where the capability is
// enabled (the values are its marker, whose presence alone the registry
// reads); nothing where it is not or the values leave workspaces off.
func platformRecordInputs(ctx context.Context, r Report, read Reader) (map[string]any, error) {
	if !r.enabled(AgentPlatform) {
		return nil, nil
	}
	ws, err := readWorkspaces(ctx, read, r.Installation)
	if err != nil || ws == nil {
		return nil, err
	}
	return map[string]any{InputsInstallation: map[string]any{"workspaces": ws.Input()}}, nil
}

// readWorkspaces reads whether inst's agent-platform values on record turn
// workspaces on, with the manager's base URL and client where they name
// them: nil where the patch does not exist, carries no workspaces switch or
// turns it off; an installation without a configs repository has none.
func readWorkspaces(ctx context.Context, read Reader, inst Installation) (*Workspaces, error) {
	if inst.Repositories.Configs == "" {
		return nil, nil
	}
	data, err := read(ctx, inst.Repositories.Configs, AgentPlatformPatchPath(inst.Name))
	if errors.Is(err, gh.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the workspaces on record: %w", err)
	}
	var patch struct {
		Workspaces struct {
			Enabled bool `yaml:"enabled"`
		} `yaml:"workspaces"`
		WorkspaceManager struct {
			OAuth struct {
				BaseURL string `yaml:"baseURL"`
				Dex     struct {
					ClientID string `yaml:"clientID"`
				} `yaml:"dex"`
			} `yaml:"oauth"`
		} `yaml:"workspace-manager"`
	}
	if err := yaml.Unmarshal([]byte(data), &patch); err != nil {
		return nil, fmt.Errorf("the workspaces on record: %s: %w", AgentPlatformPatchPath(inst.Name), err)
	}
	if !patch.Workspaces.Enabled {
		return nil, nil
	}
	return &Workspaces{Enabled: true, BaseURL: patch.WorkspaceManager.OAuth.BaseURL, ClientID: patch.WorkspaceManager.OAuth.Dex.ClientID}, nil
}
