package plan

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

func TestHubSecretReferencesHaveManifests(t *testing.T) {
	const missingManifest = "no matching Secret manifest"
	const root = "management-clusters/hazel/extras/kustomization.yaml"
	const secretPath = "management-clusters/hazel/extras/secrets/client.yaml"
	const secretManifest = "kind: Secret\nmetadata:\n  name: external-client\n  namespace: giantswarm\nstringData:\n  client-secret: GENERATED(client-secret)\n"
	const dexPath = "installations/hazel/apps/dex-app/configmap-values.yaml.patch"
	const dexValues = "oidc:\n  extraStaticClients:\n    - id: external\n      secretRef:\n        name: external-client\n        key: client-secret\n"
	for _, test := range []struct {
		name              string
		planned, recorded map[string]string
		readError         error
		want              string
	}{
		{name: "missing Secret", want: missingManifest},
		{name: "Secret in the same plan", planned: map[string]string{secretPath: secretManifest}},
		{name: "Secret in recorded extras", recorded: map[string]string{root: "resources: [./secrets/]\n", "management-clusters/hazel/extras/secrets/kustomization.yaml": "resources: [client.yaml]\n", secretPath: secretManifest}},
		{name: "wrong namespace", planned: map[string]string{secretPath: strings.ReplaceAll(secretManifest, "namespace: giantswarm", "namespace: other")}, want: missingManifest},
		{name: "wrong kind", planned: map[string]string{secretPath: strings.ReplaceAll(secretManifest, "kind: Secret", "kind: ConfigMap")}, want: missingManifest},
		{name: "Secret on another installation", planned: map[string]string{strings.ReplaceAll(secretPath, "hazel", "elsewhere"): secretManifest}, want: missingManifest},
		{name: "unreadable record", readError: errors.New("forbidden"), want: missingManifest},
		{name: "missing resource", planned: map[string]string{secretPath: secretManifest, root: "resources: [missing-secret.yaml]\n"}, want: "whose file is neither in this plan nor on record"},
		{name: "included resource in the plan", planned: map[string]string{secretPath: secretManifest, root: "resources: [secrets/client.yaml]\n"}},
		{name: "resource on record", planned: map[string]string{secretPath: secretManifest, root: "resources: [other.yaml]\n"}, recorded: map[string]string{"management-clusters/hazel/extras/other.yaml": "kind: ConfigMap\nmetadata: {name: other}\n"}},
		{name: "remote resource", planned: map[string]string{secretPath: secretManifest, root: "resources: [https://github.com/example/bases//extras]\n"}},
		{name: "recorded namespace transformer", recorded: map[string]string{root: "namespace: giantswarm\nresources: [secrets/client.yaml]\n", secretPath: strings.ReplaceAll(secretManifest, "  namespace: giantswarm\n", "")}},
		{name: "planned namespace transformer", planned: map[string]string{root: "namespace: giantswarm\nresources: [secrets/client.yaml]\n", secretPath: strings.ReplaceAll(secretManifest, "  namespace: giantswarm\n", "")}},
		{name: "cyclic kustomization", recorded: map[string]string{root: "resources: [./, secrets/client.yaml]\n", secretPath: secretManifest}},
		{name: "several documents", planned: map[string]string{secretPath: "kind: ConfigMap\nmetadata: {name: unrelated}\n---\n" + secretManifest}},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := map[string]render.File{}
			for name, content := range test.planned {
				files[name] = render.File{Content: []byte(content)}
			}
			definition := pairDefinition(nil)
			definition.Render = func(any, map[string]string, render.Mode) (*render.Result, error) {
				return &render.Result{Files: render.Fileset{renderedHub: files, render.Repository("giantswarm/example-configs"): {dexPath: render.File{Content: []byte(dexValues)}}}}, nil
			}
			read := func(_ context.Context, repository, file string) (string, error) {
				if repository == hubMCs {
					if content, found := test.recorded[file]; found {
						return content, nil
					}
					if test.readError != nil {
						return "", test.readError
					}
				}
				return "", gh.ErrNotFound
			}
			for _, content := range []bool{false, true} {
				planned := Build(t.Context(), Options{Definition: definition, Installation: hazel, Hub: hazel, Read: read, Content: content})
				if planned.Refused != "" {
					t.Fatal(planned.Refused)
				}
				refusal := planned.ReferenceRefusal()
				if test.want == "" && refusal != "" || test.want != "" && !strings.Contains(refusal, test.want) {
					t.Fatalf("content %v: refusal %q, want %q", content, refusal, test.want)
				}
			}
		})
	}
}
