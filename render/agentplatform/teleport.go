package agentplatform

import (
	"bytes"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The Teleport objects the tunnel joins with, rendered into teleport-fleet as
// operator CRDs under the hub's directory: per tunnelled app of a private target
// a role pinned to the one Teleport app, the bot holding it, the provision token
// admitting the hub's RemoteApp ServiceAccount by the hub's JWKS, and the
// workload identity whose SVID the tunnel serves; per hub the trust-bundle
// singleton, where teleport-fleet does not carry it yet. Nothing here runs
// against Teleport: teleport-fleet's Helm release applies the files. Shield
// reviews this file (CODEOWNERS).

const (
	teleportFleet     = render.Repository("giantswarm/teleport-fleet")
	teleportNamespace = "teleport"
	// teleportTemplates is where teleport-fleet's production chart reads templates; a
	// directory per hub keeps one hub's tunnels apart from another's.
	teleportTemplates = "kubernetes/envs/prod/templates/tunnelport/"
	// remoteAppLabel and trustBundleLabel are the workload-identity selector keys of
	// the two bot families. Different keys keep a leaked credential of one family
	// from minting the other's SVIDs.
	remoteAppLabel   = "remoteapp"
	trustBundleLabel = "trust-bundle"
	// trustBundleServiceAccount is the ServiceAccount the tunnelport chart renders
	// for its trust-bundle tbot in the release namespace.
	trustBundleServiceAccount = "tunnelport-trust-bundle"
	// tunnelLogin is the role's placeholder login: Teleport refuses a role without
	// one, and the application tunnel never consumes it.
	tunnelLogin = "tunnelport"
)

// teleportObjects renders the hub's Teleport objects into teleport-fleet.
func (in *Input) teleportObjects(r *render.Result) {
	if !in.hasPrivateTarget() {
		return
	}
	hub := in.Installation.Name
	dir := teleportTemplates + hub + "/"
	jwks := in.Installation.Federation.Tunnel.JWKS
	if !in.Installation.Federation.Tunnel.TrustBundleProvisioned {
		// The singleton's SVID is never presented to a verifier, only its CA chain
		// is: no SANs, and its own selector key.
		name, bot := trustBundleServiceAccount+"-"+hub, trustBundleServiceAccount+"-bot-"+hub
		r.Add(teleportFleet, dir+"trust-bundle.yaml", teleportFile(
			"# The trust-bundle singleton of the hub "+hub+": its tbot mints the SPIFFE bundle every caller\n# on the hub verifies the tunnels' SVIDs with. It reaches no application.\n",
			teleportRole(name, nil, trustBundleLabel, name),
			teleportBot(bot, name),
			teleportToken(trustBundleTokenName(hub), bot, jwks, platformNamespace+":"+trustBundleServiceAccount),
			teleportWorkloadIdentity(name, trustBundleLabel, name, bot, false),
		))
	}
	for _, t := range in.Installation.Federation.Targets {
		if !t.Private {
			continue
		}
		for _, app := range t.tunnelledApps() {
			name := t.appName(app.name)
			r.Add(teleportFleet, dir+name+".yaml", teleportFile(
				"# The tunnel from the hub "+hub+" to "+t.Installation+"'s "+app.name+": the role reaches the one Teleport app the\n# target advertises as "+name+" and nothing else; the token admits the hub's RemoteApp ServiceAccount.\n",
				teleportRole(name+"-tunnel", render.Map{e("cluster", []string{t.Installation}), e("app", []string{app.name})}, remoteAppLabel, name),
				teleportBot(name+"-bot", name+"-tunnel"),
				teleportToken(name+"-bot-token", name+"-bot", jwks, platformNamespace+":"+name),
				teleportWorkloadIdentity(name+"-svid", remoteAppLabel, name, name+"-bot", true),
			))
		}
	}
}

// teleportObject is one operator CRD: metadata in the operator's namespace, labels when given.
func teleportObject(apiVersion, kind, name string, labels, spec render.Map) render.Map {
	meta := render.Map{e("name", name), e("namespace", teleportNamespace)}
	if labels != nil {
		meta = append(meta, e("labels", labels))
	}
	return render.Map{e("apiVersion", apiVersion), e("kind", kind), e("metadata", meta), e("spec", spec)}
}

// teleportRole binds application access (appLabels, nil for the trust bundle) to
// SVID issuance for the one workload identity the selector matches.
func teleportRole(name string, appLabels render.Map, selectorKey, selectorValue string) render.Map {
	allow := render.Map{}
	if appLabels != nil {
		allow = append(allow, e("app_labels", appLabels))
	}
	allow = append(allow,
		e("workload_identity_labels", render.Map{e(selectorKey, []string{selectorValue})}),
		e("rules", []render.Map{{e("resources", []string{"workload_identity"}), e("verbs", []string{"list", "read"})}}),
		e("logins", []string{tunnelLogin}),
	)
	return teleportObject("resources.teleport.dev/v1", "TeleportRoleV7", name, nil, render.Map{e("allow", allow)})
}

func teleportBot(name, role string) render.Map {
	return teleportObject("resources.teleport.dev/v1", "TeleportBotV1", name, nil, render.Map{e("roles", []string{role})})
}

// teleportToken admits one ServiceAccount of the hub cluster by the kubernetes
// join method, the hub's JWKS verifying the projected token.
func teleportToken(name, bot, jwks, serviceAccount string) render.Map {
	return teleportObject("resources.teleport.dev/v2", "TeleportProvisionToken", name, nil, render.Map{
		e("roles", []string{"Bot"}), e("bot_name", bot), e("join_method", "kubernetes"),
		e("kubernetes", render.Map{
			e("type", "static_jwks"),
			e("static_jwks", render.Map{e("jwks", jwks)}),
			e("allow", []render.Map{{e("service_account", serviceAccount)}}),
		}),
	})
}

// teleportWorkloadIdentity is the SVID a bot mints. A served identity carries the
// DNS SANs of the RemoteApp's Service, templated off the join attributes so they
// follow the Service wherever the RemoteApp lives; the file sits in a Helm chart,
// so the Teleport template is written as a Helm string literal.
func teleportWorkloadIdentity(name, labelKey, labelValue, bot string, served bool) render.Map {
	spiffe := render.Map{e("id", "/bot/"+bot)}
	if served {
		const sa = "{{ join.kubernetes.service_account.name }}.{{ join.kubernetes.service_account.namespace }}"
		var sans []string
		for _, suffix := range []string{".svc.cluster.local", ".svc", ""} {
			sans = append(sans, `{{ "`+sa+suffix+`" }}`)
		}
		spiffe = append(spiffe, e("x509", render.Map{e("dns_sans", sans)}))
	}
	return teleportObject("resources.teleport.dev/v1", "TeleportWorkloadIdentityV1", name,
		render.Map{e(labelKey, labelValue)}, render.Map{e("spiffe", spiffe)})
}

// teleportFile is one file of the hub's directory: the header, a note, and the
// objects as YAML documents.
func teleportFile(note string, objects ...render.Map) render.File {
	docs := make([][]byte, 0, len(objects))
	for _, o := range objects {
		docs = append(docs, render.MustYAML(o))
	}
	return render.File{Content: append([]byte(fileHeader+note), bytes.Join(docs, []byte("---\n"))...)}
}
