package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// SuppliedSecretFields names the secret values the person supplies at commit
// for this installation, by field: the model key where the policy has the
// platform team supply it, the Slack app's credentials where the gateway
// runs. A dry run passes Supplied markers for exactly these fields and lists
// them by name; no value ever appears in a dry run.
func (in *Input) SuppliedSecretFields() []string { return in.suppliedSecretFields() }

// Supplied is the marker a dry run renders in place of a supplied secret
// value: the commit step replaces it with the value the person supplies.
func Supplied(field string) string { return render.Supplied(field) }

// SuppliedMarkers is the secrets map of a dry run: a marker per supplied field.
func (in *Input) SuppliedMarkers() map[string]string {
	fields := in.SuppliedSecretFields()
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		m[f] = Supplied(f)
	}
	return m
}

// CustomerActions names what the rollout needs from the customer beyond the
// pull requests. The one thing the platform leaves to the customer — the
// model key where the policy does not have the platform team supply it — is
// a live action of the runtime feature (actions), not a step before it.
func (in *Input) CustomerActions() []render.CustomerAction { return nil }
