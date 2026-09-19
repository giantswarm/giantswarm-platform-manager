package tools

import (
	"regexp"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/actions"
	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
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
