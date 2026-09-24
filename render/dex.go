package render

// Dex as the dex-app chart runs it, where the definitions' probes look for it.
const (
	// DexPodSelector selects Dex's pods: the chart's name label, which the
	// dex-k8s-authenticator's pods do not carry.
	DexPodSelector = "app.kubernetes.io/name=dex"
	// DexContainer is the container of those pods that runs Dex.
	DexContainer = "dex"
)

// DexAuthProbe is the live check that Dex knows client and its redirect URI:
// the authorization request on dexHost's /auth, taken through the connector
// step (Expectation.DexConnectorStep). The connector answers a client it knows
// with a registered redirect URI with a redirect to the identity provider (a
// password connector with its form), an unknown client with 404 and a
// redirect URI not registered for the client with 400.
func DexAuthProbe(id, feature, dexHost, client, redirectURI string) Probe {
	return Probe{ID: id, Feature: feature, Kind: HTTP,
		URL: "https://" + dexHost + "/auth?client_id=" + client + "&redirect_uri=" + redirectURI + "&response_type=code&scope=openid",
		Expect: Expectation{Statuses: []int{200, 302}, DexConnectorStep: true,
			Note: "Dex knows client " + client + " and its redirect URI when the connector its /auth answer names redirects to the identity provider or serves its login form; an unknown client answers 404, an unregistered redirect URI 400"}}
}

// DexSecretLoadedProbe is the live check that Dex holds the current secret of
// a client whose secret it reads from the Secret secret in namespace, Dex's:
// Dex reads a referenced client secret into its environment only when its
// container starts, so a container started before the Secret last changed
// holds the old secret, and the client's sign-ins fail with invalid_client
// until Dex restarts (dex-app 3.2.3 restarts the container on the change,
// before it nothing does). client names the client in the note. The check
// reads when the Secret changed, never its value.
func DexSecretLoadedProbe(id, feature, namespace, secret, client string) Probe {
	return Probe{ID: id, Feature: feature, Kind: SecretLoaded, Namespace: namespace, Resource: "Secret", Name: secret,
		Expect: Expectation{Pods: DexPodSelector, Container: DexContainer,
			Note: "Dex reads the secret of client " + client + " only when its container starts; one started before the Secret changed holds the old secret and the client's sign-ins fail until Dex restarts (dex-app 3.2.3 restarts it on the change)"}}
}
