package render

// tunnelport's facts every definition and the registry share: where the
// fleet's tunnels are declared, and what the tunnelport chart fixes for every
// tunnel it renders.
const (
	// TeleportFleet declares the fleet's tunnels in TunnelportValuesPath:
	// tunnelport.tunnels, each a Teleport app pinned by its appLabels with one
	// token per consumer, and tunnelport.consumers, each consumer's
	// installNamespace, where its RemoteApps and their Services live.
	TeleportFleet        Repository = "giantswarm/teleport-fleet"
	TunnelportValuesPath string     = "kubernetes/envs/prod/values.yaml"
	// TunnelPort is the port of every tunnel's Service: ghostunnel terminates
	// TLS there with the consumer's SPIFFE SVID.
	TunnelPort = "8443"
	// TunnelBundleKey is the key of the trust bundle in the Secret the
	// tunnelport release's trust bundle writes (trustBundle.secretName), next
	// to the SVID and its private key.
	TunnelBundleKey = "svid_bundle.pem"
	// DexTunnelApp is the app label of a tunnel to an installation's Dex.
	DexTunnelApp = "dex"
)

// TunnelHost is the in-cluster host of a tunnel's Service, service in
// namespace, on TunnelPort.
func TunnelHost(service, namespace string) string {
	return service + "." + namespace + ".svc.cluster.local:" + TunnelPort
}
