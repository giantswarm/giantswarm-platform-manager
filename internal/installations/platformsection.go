package installations

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// The files of a portal on record the customer portal's platformSection is
// read from, as the definition's x-files names them: the portal's own
// app-config, and the agent-platform Component's app-config fragment and its
// credentials Secret for the chat beside it.
const (
	portalAppConfigFile       = "backstage:app-config"
	portalFragmentFile        = "backstage:agent-platform/app-config"
	portalChatCredentialsFile = "backstage:file:agent-platform/ai-chat-credentials.enc.yaml" // #nosec G101 -- a file name, not a value
)

// The customer portal's platformSection facts, as its schema names them.
const (
	sectionComponentLists  = "componentLists"
	sectionAIChat          = "aiChat"
	sectionChatProvider    = "chatProvider"
	sectionChatCredentials = "chatCredentials"
	sectionAppConfig       = "appConfig"
)

// chatProviderKey is where the chat's block names its provider, and
// chatProviderAnthropic the provider of a block that names none: Claude on
// Anthropic's API.
const (
	chatProviderKey       = "aiChat.anthropic.provider"
	chatProviderAnthropic = "anthropic"
)

// portalExtensions is the portal's extension list, handed over by anchor:
// the customer-portal definition includes the shared list with the
// platform's section itself while the Component's fragment carries none, so
// the record's list is not kept.
const portalExtensions = "app.extensions"

// handedPortalKeys are the key paths of the portal's app-config the
// customer-portal definition hands to the agent-platform definition's
// Component: its removals of kind other-definition in the app-config. The
// definitions are embedded, so a removals.yaml that does not read fails the
// build's tests, never a call.
var handedPortalKeys = func() []string {
	keys, err := definitions.HandedKeys(CustomerPortal, definitions.KindBackstage+":app-config")
	if err != nil {
		panic(err)
	}
	return keys
}()

// hubPortalKeys are the key paths of the portal's app-config that are
// sections of the hub's Dev Portal: the customer-portal definition's
// removals of kind hub in the app-config.
var hubPortalKeys = func() []string {
	keys, err := definitions.HubKeys(CustomerPortal, definitions.KindBackstage+":app-config")
	if err != nil {
		panic(err)
	}
	return keys
}()

// isHubPortal says whether appConfig, a portal's app-config on record, is
// the hub's Dev Portal kept by hand: its extension list is a literal list of
// its own, not the shared include, and it carries a section of the hub's Dev
// Portal with a value (one that is neither null nor empty).
func isHubPortal(appConfig map[string]any) bool {
	if list, _ := lookup(appConfig, splitKey(portalExtensions)); !isList(list) {
		return false
	}
	for _, key := range hubPortalKeys {
		if v, ok := lookup(appConfig, splitKey(key)); ok && hasValue(v) {
			return true
		}
	}
	return false
}

func isList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// hasValue says whether v carries a value: not null, not an empty mapping,
// list or string.
func hasValue(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case string:
		return v != ""
	}
	return true
}

// portalPlatformSection is the customer portal's platformSection from the
// portal's files on record, read as the caller: whether the Component's
// fragment carries the extension list (componentLists: it owns the portal's
// lists), whether the portal runs the chat (the fragment, else the main
// app-config, carries the aiChat block) and on which provider (the block's
// aiChat.anthropic.provider, the fragment's first, as the agent-platform
// definition reads it), whether the Component's credentials Secret for the
// chat is on record (chatCredentials: its presence, nothing decrypted), and
// every handed key the main app-config carries but the extension list, at
// its path with its value (appConfig). A file not on record carries nothing.
// The same read answers whether the portal on record is the hub's Dev Portal
// kept by hand (isHubPortal).
func portalPlatformSection(ctx context.Context, r Report, read Reader) (section map[string]any, hubPortal bool, err error) {
	c, _ := FindCapability(CustomerPortal)
	s, err := c.inputSchema()
	if err != nil {
		return nil, false, err
	}
	// The customer portal's files are the installation's own: it hosts the portal.
	files := newReadBackFiles(ctx, read, r.Installation, r.Name)
	doc := func(file string) (map[string]any, error) {
		spec, ok := s.Files[file]
		if !ok {
			return nil, fmt.Errorf("%s: schema: x-files does not declare %q", c.Name, file)
		}
		return files.doc(file, spec)
	}
	appConfig, err := doc(portalAppConfigFile)
	if err != nil {
		return nil, false, err
	}
	fragment, err := doc(portalFragmentFile)
	if err != nil {
		return nil, false, err
	}
	credentials, err := doc(portalChatCredentialsFile)
	if err != nil {
		return nil, false, err
	}
	_, lists := lookup(fragment, splitKey(portalExtensions))
	_, fragmentChat := lookup(fragment, splitKey(sectionAIChat))
	_, ownChat := lookup(appConfig, splitKey(sectionAIChat))
	provider := chatProviderAnthropic
	for _, d := range []map[string]any{fragment, appConfig} {
		if v, _ := lookup(d, splitKey(chatProviderKey)); v != nil {
			provider = fmt.Sprint(v)
			break
		}
	}
	kept := map[string]any{}
	for _, key := range handedPortalKeys {
		if key == portalExtensions || strings.HasPrefix(key, portalExtensions+".") || strings.HasPrefix(key, portalExtensions+"[") {
			continue
		}
		if v, ok := lookup(appConfig, splitKey(key)); ok {
			set(kept, splitKey(key), v)
		}
	}
	return map[string]any{sectionComponentLists: lists, sectionAIChat: fragmentChat || ownChat, sectionChatProvider: provider,
		sectionChatCredentials: credentials != nil, sectionAppConfig: kept}, isHubPortal(appConfig), nil
}
