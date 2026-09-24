package installations

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// The customer portal's platformSection is read from the portal's two files
// on record: the handed keys the app-config carries, at their paths with
// their values — the extension list aside, which the definition includes by
// anchor — whether the Component's fragment carries the extension list, and
// whether the fragment or the app-config carries the chat. Without the
// fragment on record it owns no list; with neither file, nothing is kept.
func TestPortalPlatformSection(t *testing.T) {
	const mcs, muster = "acme/mcs", "https://muster.maple.acme.test/mcp"
	appConfigPath, fragmentPath := mcs+":"+PortalConfigPath(fixtureInstallation), mcs+":management-clusters/"+fixtureInstallation+"/extras/backstage/agent-platform/app-config.yaml"
	appConfig := "data:\n  values: |\n    backstage:\n      appConfig: |\n" +
		"        app:\n          extensions:\n            - page:catalog\n            - page:agent-platform\n" +
		"        muster:\n          installations:\n            - url: " + muster + "\n" +
		"        agentPlatform:\n          skills:\n            repositories:\n              - https://github.com/example/agent-skills\n" +
		"        aiChat:\n          model: claude-sonnet-5\n" +
		"        backend:\n          baseUrl: https://portal.maple.acme.test\n          actions:\n            pluginSources: [catalog]\n"
	handKept := "data:\n  app-config.agent-platform.yaml: |\n    agentPlatform:\n      kagent:\n        installations: {}\n"
	ownsLists := "data:\n  app-config.agent-platform.yaml: |\n    app:\n      extensions:\n        $include: shared-config.yaml#extensionsAgentPlatform\n    aiChat:\n      model: claude-sonnet-5\n"
	bare := "data:\n  values: |\n    backstage:\n      appConfig: |\n        app:\n          title: Dev Portal\n"
	report := Report{Installation: Installation{Name: fixtureInstallation, Repositories: Repositories{ManagementClusters: mcs}}}
	kept := map[string]any{
		"muster":        map[string]any{"installations": []any{map[string]any{"url": muster}}},
		"agentPlatform": map[string]any{"skills": map[string]any{"repositories": []any{"https://github.com/example/agent-skills"}}},
		sectionAIChat:   map[string]any{"model": "claude-sonnet-5"},
		"backend":       map[string]any{"actions": map[string]any{"pluginSources": []any{"catalog"}}},
	}
	section := func(lists, chat bool, appConfig map[string]any) map[string]any {
		return map[string]any{sectionComponentLists: lists, sectionAIChat: chat, sectionAppConfig: appConfig}
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[string]any
	}{
		{"the fragment in the hand-kept shape", map[string]string{appConfigPath: appConfig, fragmentPath: handKept}, section(false, true, kept)},
		{"no fragment on record", map[string]string{appConfigPath: appConfig}, section(false, true, kept)},
		{"the fragment owns the lists, the chat in it", map[string]string{appConfigPath: bare, fragmentPath: ownsLists}, section(true, true, map[string]any{})},
		{"no portal on record", map[string]string{}, section(false, false, map[string]any{})},
	} {
		got, err := portalPlatformSection(context.Background(), report, files(tc.files))
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v %v, want %v", tc.name, got, err, tc.want)
		}
	}
	unreadable := errors.New("forbidden")
	if _, err := portalPlatformSection(context.Background(), report, func(context.Context, string, string) (string, error) { return "", unreadable }); !errors.Is(err, unreadable) {
		t.Errorf("an unreadable portal refuses the comparison: %v", err)
	}
}
