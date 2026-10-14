package plan

import (
	"strings"
	"testing"
)

// A plan that removes sections of the hub's Dev Portal the record carries
// with a value is held, naming the sections by file in the order the plan
// lists them, and the way out; a plan that removes none commits.
func TestHubRefusal(t *testing.T) {
	hubShaped := Installation{Name: "hazel", HubSections: []string{
		"backstage:app-config:pagerDuty",
		"backstage:app-config:proxy.endpoints./circleci/api",
		"backstage:app-config:backend.database",
		"backstage:user-values:database",
	}}
	got := hubShaped.HubRefusal()
	want := "the portal on record carries sections of the hub's Dev Portal that this definition renders for no shared portal (app-config: pagerDuty, proxy.endpoints./circleci/api, backend.database; user-values: database), and a commit would remove them; the hub's shape keeps them where the portal is kept by hand, listing app.extensions literally"
	if got != want {
		t.Errorf("the hub's portal:\n got %s\nwant %s", got, want)
	}
	if strings.Contains(got, "  ") {
		t.Errorf("%q", got)
	}
	if got := (Installation{Name: "maple"}).HubRefusal(); got != "" {
		t.Errorf("a customer's portal: %q", got)
	}
}

// A removal key's file is its fileset for a backstage key and its prefix
// otherwise; the path keeps every colon after the file.
func TestSplitKey(t *testing.T) {
	for _, tc := range []struct{ key, file, path string }{
		{"backstage:app-config:gs.github", "app-config", "gs.github"},
		{"backstage:user-values:database", "user-values", "database"},
		{"backstage:app-config:app.extensions[14].entity-card:catalog/labels", "app-config", "app.extensions[14].entity-card:catalog/labels"},
		{"configmap:kagent.modelConfigs", "configmap", "kagent.modelConfigs"},
	} {
		if file, path := splitKey(tc.key); file != tc.file || path != tc.path {
			t.Errorf("%s: %q %q, want %q %q", tc.key, file, path, tc.file, tc.path)
		}
	}
}
