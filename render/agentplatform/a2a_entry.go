package agentplatform

import (
	"net/url"
	"slices"
	"strings"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

const (
	a2aAuthenticatorKey       = "dexK8SAuthenticator"
	a2aAuthorizationCodeGrant = "authorization_code"
	a2aClientCredentialsGrant = "client_credentials"
	a2aExchangeClient         = "klaus-gateway-exchange"
	a2aAudienceClient         = "klaus-gateway"
	a2aApplicationAudience    = "klaus-gateway-app"
	a2aExchangeGrant          = "urn:ietf:params:oauth:grant-type:token-exchange"
)

type A2AEntry struct {
	Enabled           bool             `json:"enabled"`
	Issuers           []A2AIssuer      `json:"issuers"`
	StaffConnectors   []string         `json:"staffConnectors"`
	ExchangeSecretRef A2ASecretRef     `json:"exchangeSecretRef"`
	Applications      []A2AApplication `json:"applications"`
}

type A2AIssuer struct {
	Name           string            `json:"name"`
	Issuer         string            `json:"issuer"`
	Audience       string            `json:"audience"`
	AllowedClients []string          `json:"allowedClients"`
	ConnectorID    string            `json:"connectorId"`
	JWKS           map[string]any    `json:"jwks"`
	Card           map[string]string `json:"card,omitempty"`
}

type A2ASecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type A2AApplication struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	SecretRef A2ASecretRef `json:"secretRef"`
}

func (in *Input) checkA2AEntry() error {
	entry := in.A2AEntry
	if !entry.Enabled {
		return nil
	}
	if in.Installation.ChartLine != lineFour || !in.kagent() || !in.agentManager() {
		return refuse("klausGateway.a2aServer.enabled requires kagent and agent-manager on the 4 chart line")
	}
	if len(entry.Issuers) == 0 || len(entry.StaffConnectors) == 0 {
		return refuse("klausGateway.a2aServer requires trusted issuers and explicit staffConnectors")
	}
	if entry.ExchangeSecretRef.Name == "" || entry.ExchangeSecretRef.Key == "" {
		return refuse("klausGateway.a2aServer.exchangeSecretRef must name an existing Secret and key in both agent-platform and Dex's namespace")
	}
	for _, connector := range entry.StaffConnectors {
		if strings.TrimSpace(connector) == "" {
			return refuse("klausGateway.a2aServer.staffConnectors must not contain empty connector IDs")
		}
	}
	for name, subjects := range map[string]*[]Subject{
		inputPlatformUsers: in.PlatformTenant.Users,
		inputMusterViewers: in.PlatformTenant.Viewers,
	} {
		if subjects == nil {
			return refuse("klausGateway.a2aServer.enabled requires an explicit RBAC subject list at " + name)
		}
		for _, subject := range *subjects {
			if broadGroup(subject) != "" {
				return refuse("klausGateway.a2aServer.enabled refuses broad system groups at " + name)
			}
		}
	}
	names, issuers, connectors := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, issuer := range entry.Issuers {
		if issuer.Issuer != "https://"+in.host("dex") && in.Installation.KubernetesUsernameClaim != "sub" {
			return refuse("outside A2A issuers require Kubernetes usernames derived from Dex sub; email usernames can collide with staff RBAC identities")
		}
		parsed, err := url.Parse(issuer.Issuer)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return refuse("klausGateway.a2aServer.issuers requires HTTPS issuer URLs without user information, queries or fragments")
		}
		if issuer.Name == "" || issuer.Audience == "" || issuer.ConnectorID == "" || len(issuer.AllowedClients) == 0 || len(issuer.JWKS) == 0 {
			return refuse("klausGateway.a2aServer.issuers requires name, audience, connectorId, allowedClients and jwks")
		}
		if names[issuer.Name] || issuers[issuer.Issuer] || connectors[issuer.ConnectorID] || slices.Contains(entry.StaffConnectors, issuer.ConnectorID) {
			return refuse("klausGateway.a2aServer.issuers must have unique names, issuers and connector IDs distinct from staffConnectors")
		}
		names[issuer.Name], issuers[issuer.Issuer], connectors[issuer.ConnectorID] = true, true, true
	}
	clientIDs := map[string]bool{a2aExchangeClient: true, a2aAudienceClient: true, a2aApplicationAudience: true, authenticatorClient: true, componentKagent: true, in.Installation.MusterClientID: true}
	for _, client := range in.portalAudiences() {
		clientIDs[client] = true
	}
	for _, client := range in.confidentialClients() {
		clientIDs[client.ClientID] = true
	}
	for _, client := range entry.Applications {
		if client.ID == "" || clientIDs[client.ID] || client.SecretRef.Name == "" || client.SecretRef.Key == "" {
			return refuse("klausGateway.a2aServer.applications requires unique client IDs and existing Secret references")
		}
		clientIDs[client.ID] = true
		if !slices.ContainsFunc(entry.Issuers, func(issuer A2AIssuer) bool {
			return issuer.Issuer == "https://"+in.host("dex") && issuer.Audience == a2aApplicationAudience && slices.Contains(issuer.AllowedClients, client.ID)
		}) {
			return refuse("klausGateway.a2aServer.applications requires the installation's Dex as a trusted issuer with audience klaus-gateway-app and each application in allowedClients")
		}
	}
	return nil
}

func (in *Input) a2aRoute() render.Map {
	issuers := make([]render.Map, 0, len(in.A2AEntry.Issuers))
	for _, issuer := range in.A2AEntry.Issuers {
		entry := render.Map{e("name", issuer.Name), e("issuer", issuer.Issuer), e("audience", issuer.Audience), e("allowedClients", issuer.AllowedClients), e("connectorId", issuer.ConnectorID), e("jwks", issuer.JWKS)}
		if len(issuer.Card) > 0 {
			entry = append(entry, e("card", issuer.Card))
		}
		issuers = append(issuers, entry)
	}
	secret := in.A2AEntry.ExchangeSecretRef
	return render.Map{e("enabled", true), e("issuers", issuers), e("exchange", render.Map{
		e("clientId", a2aExchangeClient), e("secretName", secret.Name), e("secretKey", secret.Key),
		e("audiences", []string{a2aAudienceClient, authenticatorClient, componentKagent}),
		e("subjectTokenType", "IdToken"),
	})}
}

func a2aSecretRef(reference A2ASecretRef) render.Map {
	return render.Map{e("name", reference.Name), e("key", reference.Key)}
}

func (in *Input) a2aDexPatch(patch *render.Map) {
	oidc := mapValue(*patch, "oidc").(render.Map)
	static := mapValue(oidc, "staticClients").(render.Map)
	extra, _ := mapValue(oidc, "extraStaticClients").(render.Map)
	for _, name := range []string{"gitopsui", "grafana", "gsCLIAuth", "happa", a2aAuthenticatorKey, "mcpCapi", "mcpKubernetes", "mcpPrometheus", "muster"} {
		values, _ := mapValue(static, name).(render.Map)
		values = withA2AEntry(values, "allowedConnectors", in.A2AEntry.StaffConnectors)
		if name == a2aAuthenticatorKey {
			peers, _ := mapValue(values, "trustedPeers").([]string)
			values = withA2AEntry(values, "trustedPeers", append(peers, a2aExchangeClient))
		}
		static = withA2AEntry(static, name, values)
	}
	for index := range extra {
		values := extra[index].Value.(render.Map)
		values = withA2AEntry(values, "grantTypes", a2aStaffGrants())
		values = withA2AEntry(values, "allowedConnectors", in.A2AEntry.StaffConnectors)
		if extra[index].Key == componentKagent {
			values = withA2AEntry(values, "trustedPeers", []string{a2aExchangeClient})
		}
		extra[index].Value = values
	}
	connectorIDs := make([]string, 0, len(in.A2AEntry.Issuers))
	owner, _ := mapValue(oidc, "giantswarm").(render.Map)
	connectorEntries, _ := mapValue(owner, "connectors").(render.Map)
	for _, issuer := range in.A2AEntry.Issuers {
		connectorIDs = append(connectorIDs, issuer.ConnectorID)
		configuration := render.Map{e("issuer", issuer.Issuer), e("clientID", issuer.Audience), e("redirectURI", "https://"+in.host("dex")+"/callback"), e("insecureEnableGroups", true)}
		if issuer.Issuer != "https://"+in.host("dex") {
			configuration = append(configuration, e("claimModifications", render.Map{e("modifyGroupNames", render.Map{e("prefix", issuer.ConnectorID+":")})}))
		}
		connectorEntries = append(connectorEntries, e(issuer.ConnectorID, render.Map{
			e("connectorType", "oidc"), e("connectorName", issuer.Name), e("grantTypes", []string{a2aExchangeGrant}),
			e("config", configuration),
		}))
	}
	extra = append(extra,
		e(a2aExchangeClient, render.Map{e("name", "A2A token exchange"), e("secretRef", a2aSecretRef(in.A2AEntry.ExchangeSecretRef)), e("grantTypes", []string{a2aExchangeGrant}), e("allowedConnectors", connectorIDs)}),
		e(a2aAudienceClient, render.Map{e("name", "A2A audience"), e("public", true), e("grantTypes", []string{a2aAuthorizationCodeGrant}), e("allowedConnectors", in.A2AEntry.StaffConnectors), e("trustedPeers", []string{a2aExchangeClient})}),
	)
	applicationIDs := make([]string, 0, len(in.A2AEntry.Applications))
	for _, client := range in.A2AEntry.Applications {
		extra = append(extra, e(client.ID, render.Map{e("name", client.Name), e("secretRef", a2aSecretRef(client.SecretRef)), e("grantTypes", []string{a2aClientCredentialsGrant})}))
		applicationIDs = append(applicationIDs, client.ID)
	}
	if len(applicationIDs) > 0 {
		extra = append(extra, e(a2aApplicationAudience, render.Map{e("name", "A2A application audience"), e("public", true), e("grantTypes", []string{a2aAuthorizationCodeGrant}), e("allowedConnectors", in.A2AEntry.StaffConnectors), e("trustedPeers", applicationIDs)}))
	}
	grants := a2aStaffGrants()
	if len(in.A2AEntry.Applications) > 0 {
		grants = append(grants, a2aClientCredentialsGrant)
	}
	oidc = withA2AEntry(oidc, "staticClients", static)
	oidc = withA2AEntry(oidc, "extraStaticClients", extra)
	oidc = withA2AEntry(oidc, "giantswarm", withA2AEntry(owner, "connectors", connectorEntries))
	oidc = withA2AEntry(oidc, "grantTypes", grants)
	*patch = withA2AEntry(*patch, "oidc", oidc)
}

func withA2AEntry(values render.Map, key string, value any) render.Map {
	for index := range values {
		if values[index].Key == key {
			values[index].Value = value
			return values
		}
	}
	return append(values, e(key, value))
}

func a2aStaffGrants() []string {
	return []string{a2aAuthorizationCodeGrant, "refresh_token", "implicit", "password", "urn:ietf:params:oauth:grant-type:device_code", a2aExchangeGrant}
}
