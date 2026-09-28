package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
)

// The GitOps repositories run amannn/action-semantic-pull-request with its
// defaults: the title is parsed with conventional-changelog-conventionalcommits'
// header pattern (type, optional scope, subject) and the type must be one of
// commitizen/conventional-commit-types. A title that fails either blocks the
// merge until a person retitles the pull request.
var (
	semanticHeader = regexp.MustCompile(`^(\w*)(?:\((.*)\))?!?: (.*)$`)
	semanticTypes  = map[string]bool{"feat": true, "fix": true, "docs": true, "style": true, "refactor": true, "perf": true, "test": true, "build": true, "ci": true, "chore": true, "revert": true}
)

// Every title the manager opens passes the check as opened, the installation
// is its scope, and the action id stays in it.
func TestPRTitleIsSemantic(t *testing.T) {
	for _, tc := range []struct {
		kind, action, detail string
		want                 string
	}{
		{actions.KindEnable, "enable-rowan-k3x9ab", "", "feat(rowan): enable agent-platform (enable-rowan-k3x9ab)"},
		{actions.KindReconcile, "reconcile-rowan-k3x9ab", "", "fix(rowan): reconcile agent-platform (reconcile-rowan-k3x9ab)"},
		{actions.KindReconcile, "reconcile-wave-k3x9ab", "stage 2 of 3", "fix(rowan): reconcile agent-platform (reconcile-wave-k3x9ab, stage 2 of 3)"},
	} {
		got := prTitle(tc.kind, rowan, installations.AgentPlatform, tc.action, tc.detail)
		if got != tc.want {
			t.Errorf("%s %q: title %q, want %q", tc.kind, tc.detail, got, tc.want)
		}
		m := semanticHeader.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("%q does not match the semantic-pull-request header pattern", got)
		}
		if !semanticTypes[m[1]] {
			t.Errorf("%q: type %q is not one the check accepts", got, m[1])
		}
		if m[2] != rowan {
			t.Errorf("%q: scope %q, want the installation", got, m[2])
		}
		if m[3] == "" || !strings.Contains(m[3], tc.action) {
			t.Errorf("%q: subject %q does not carry the action id", got, m[3])
		}
	}
}

// The pull request names what the commit loses of the encrypted files on
// record it writes over unread — the values it drops, the texts it replaces
// whole — so the person approving reads it next to the files; a commit that
// loses nothing says nothing of it.
func TestPRBodyNamesTheEncryptedValuesTheCommitLoses(t *testing.T) {
	a := &actions.Action{}
	a.Name = "reconcile-rowan-k3x9ab"
	a.Spec = actions.Spec{Kind: actions.KindReconcile, Capability: installations.CustomerPortal}
	const secrets = "management-clusters/rowan/extras/backstage/backstage/user-secrets.enc.yaml"
	p := plan.Installation{Name: rowan, Files: []plan.File{
		{Path: secrets, Change: plan.ChangeUpdate, Dropped: []string{"stringData.EXTERNAL_ACCESS_MCP_TOKEN"}, Replaced: []string{"stringData.values"}},
		{Path: "management-clusters/rowan/extras/backstage/backstage/app-config.yaml", Change: plan.ChangeUpdate},
	}}
	body := prBody(a, p, nil)
	for _, want := range []string{"Encrypted values on record this commit writes over unread", secrets + ": drops stringData.EXTERNAL_ACCESS_MCP_TOKEN", "replaces stringData.values whole"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	p.Files = p.Files[1:]
	if body := prBody(a, p, nil); strings.Contains(body, "Encrypted values on record") {
		t.Errorf("a commit that loses nothing names a loss:\n%s", body)
	}
}
