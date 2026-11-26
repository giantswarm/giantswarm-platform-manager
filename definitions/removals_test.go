package definitions

import "testing"

// A removal of kind deleted-file names one YAML file under extras/: a key
// with a YAML path, a placeholder, a path outside extras/ or a kustomization
// would delete nothing or the wrong file, so Removals refuses it and
// TestEveryDefinitionParses fails on it.
func TestDeletedFileKeyNamesOneFileUnderExtras(t *testing.T) {
	for key, ok := range map[string]bool{
		"extras:agent-platform/secrets/kagent-credentials-revision.yaml":            true,
		"extras:agent-platform/secrets/kagent-credentials-revision.yaml stringData": false,
		"extras:mcp-<name>/oauth-credentials.enc.yaml":                              false,
		"configmap:agent-platform/secrets/x.yaml":                                   false,
		"extras:../other/x.yaml":                                                    false,
		"extras:agent-platform/secrets":                                             false,
		"extras:agent-platform/secrets/kustomization.yaml":                          false,
	} {
		if got := deletedFileKey(key) == ""; got != ok {
			t.Errorf("%s: accepted %v, want %v (%s)", key, got, ok, deletedFileKey(key))
		}
	}
}
