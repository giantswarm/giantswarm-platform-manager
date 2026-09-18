package plan

import (
	"errors"
	"strings"
	"testing"
)

const renderedKustomization = `# Rendered by giantswarm-platform-manager, agent-platform definition. Do not edit by hand:
# the next reconcile writes it again from the installation's inputs.
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main
  - ./secrets
patches:
  - patch: |-
      - op: replace
        path: /spec/ref/semver
        value: ">=4.0.0 <5.0.0"
    target:
      kind: OCIRepository
      name: agent-platform
`

func TestKeepAppendsOtherOwnersEntriesAfterThePlatforms(t *testing.T) {
	current := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main
  # the tunnel's operator
  - ./tunnelport
  - ./secrets
  # agents installed via the generic agent chart
  - ./agents
  - ./mcpservers
components:
  - ./observability
patches:
  - patch: |-
      - op: replace
        path: /spec/ref/semver
        value: ">=3.0.0 <4.0.0"
    target:
      kind: OCIRepository
      name: agent-platform
`
	got, kept, err := keep([]byte(renderedKustomization), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	want := []Kept{{ListResources, "./tunnelport"}, {ListResources, "./agents"}, {ListResources, "./mcpservers"}, {ListComponents, "./observability"}}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("kept[%d] = %v, want %v", i, kept[i], want[i])
		}
	}
	s := string(got)
	for _, frag := range []string{
		"# Rendered by giantswarm-platform-manager",
		"resources:\n  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n  - ./secrets\n  - ./tunnelport\n  - ./agents\n  - ./mcpservers\n",
		"components:\n  - ./observability\n",
		"value: \">=4.0.0 <5.0.0\"",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("merged file lacks %q:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "3.0.0") || strings.Contains(s, "the tunnel's operator") {
		t.Errorf("merged file carries the current file's own values or comments:\n%s", s)
	}
}

func TestKeepLeavesTheRenderAsItIsWithNothingToKeep(t *testing.T) {
	current := "resources:\n  - ./secrets\n  - https://github.com/giantswarm/management-cluster-bases//extras/agent-platform?ref=main\n"
	got, kept, err := keep([]byte(renderedKustomization), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	if kept != nil || string(got) != renderedKustomization {
		t.Errorf("kept %v; content changed:\n%s", kept, got)
	}
}

func TestKeepRefusesACurrentFileThatIsNoMapping(t *testing.T) {
	if _, _, err := keep([]byte(renderedKustomization), []byte("- a\n- b\n")); !errors.Is(err, errNoMapping) {
		t.Errorf("err = %v, want errNoMapping", err)
	}
}
