package render

import "github.com/Masterminds/semver/v3"

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

// DexAppLine2 is the first dex-app of the 2.x line that carries the 3.x Dex
// features up to DexAppLine2Carries — the referenced client Secrets, the roll
// on a configuration change and the restart on a rotated Secret — without
// 3.0.0's code-flow-only responseTypes default.
const DexAppLine2 = "2.4.0"

// DexAppLine2Carries is the last 3.x release whose Dex features the 2.x line
// carries from DexAppLine2: a feature a later 3.x release introduced is the
// 3.x line's alone until a 2.x release takes it.
const DexAppLine2Carries = "3.2.5"

// DexAppTakes says whether dex-app version carries the Dex feature 3.x
// introduced in release since: since or later, or DexAppLine2 or later on the
// 2.x line where since is no later than DexAppLine2Carries. A pre-release of
// either takes nothing.
func DexAppTakes(version *semver.Version, since string) bool {
	if version.Major() == 2 {
		return line2Carries(since) && !version.LessThan(semver.MustParse(DexAppLine2))
	}
	return !version.LessThan(semver.MustParse(since))
}

// DexAppNeeds names the dex-app versions that carry the Dex feature 3.x
// introduced in release since, for a refusal.
func DexAppNeeds(since string) string {
	if !line2Carries(since) {
		return "dex-app " + since + " or later"
	}
	return "dex-app " + since + " or later (" + DexAppLine2 + " or later on the 2.x line)"
}

// line2Carries says whether the 2.x line carries the feature 3.x introduced
// in release since.
func line2Carries(since string) bool {
	return !semver.MustParse(since).GreaterThan(semver.MustParse(DexAppLine2Carries))
}
