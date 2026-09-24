package installations

import (
	"context"
	"fmt"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/definitions"
)

// The files of a portal on record the customer portal's platformSection is
// read from, as the definition's x-files names them: the portal's own
// app-config, and the agent-platform Component's app-config fragment beside
// it.
const (
	portalAppConfigFile = "backstage:app-config"
	portalFragmentFile  = "backstage:agent-platform/app-config"
)

// The customer portal's platformSection facts, as its schema names them.
const (
	sectionComponentLists = "componentLists"
	sectionAIChat         = "aiChat"
	sectionAppConfig      = "appConfig"
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

// portalPlatformSection is the customer portal's platformSection from the
// portal's files on record, read as the caller: whether the Component's
// fragment carries the extension list (componentLists: it owns the portal's
// lists), whether the portal runs the chat (the fragment, else the main
// app-config, carries the aiChat block), and every handed key the main
// app-config carries but the extension list, at its path with its value
// (appConfig). A file not on record carries nothing.
func portalPlatformSection(ctx context.Context, r Report, read Reader) (map[string]any, error) {
	c, _ := FindCapability(CustomerPortal)
	s, err := c.inputSchema()
	if err != nil {
		return nil, err
	}
	docs := map[string]map[string]any{}
	doc := func(file string) (map[string]any, error) {
		spec, ok := s.Files[file]
		if !ok {
			return nil, fmt.Errorf("%s: schema: x-files does not declare %q", c.Name, file)
		}
		return readBackDoc(ctx, read, r.Installation, file, spec, docs)
	}
	appConfig, err := doc(portalAppConfigFile)
	if err != nil {
		return nil, err
	}
	fragment, err := doc(portalFragmentFile)
	if err != nil {
		return nil, err
	}
	_, lists := lookup(fragment, splitKey(portalExtensions))
	_, fragmentChat := lookup(fragment, splitKey(sectionAIChat))
	_, ownChat := lookup(appConfig, splitKey(sectionAIChat))
	kept := map[string]any{}
	for _, key := range handedPortalKeys {
		if key == portalExtensions || strings.HasPrefix(key, portalExtensions+".") || strings.HasPrefix(key, portalExtensions+"[") {
			continue
		}
		if v, ok := lookup(appConfig, splitKey(key)); ok {
			set(kept, splitKey(key), v)
		}
	}
	return map[string]any{sectionComponentLists: lists, sectionAIChat: fragmentChat || ownChat, sectionAppConfig: kept}, nil
}
