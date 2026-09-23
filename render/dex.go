package render

// Dex as the dex-app chart runs it, where the definitions' probes look for it.
const (
	// DexPodSelector selects Dex's pods: the chart's name label, which the
	// dex-k8s-authenticator's pods do not carry.
	DexPodSelector = "app.kubernetes.io/name=dex"
	// DexContainer is the container of those pods that runs Dex.
	DexContainer = "dex"
)

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
