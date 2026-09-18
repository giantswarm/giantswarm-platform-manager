package agentplatform

import (
	"fmt"

	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// SuppliedSecretFields names the secret values the person supplies at commit
// for this input, by field: the model key when kagent.modelKeySecret is
// managed, the client credentials of every oauth server in
// toolAccess.additionalServers. A dry run passes Supplied markers for exactly
// these fields and lists them by name; no value ever appears in a dry run.
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
// pull requests: the provider-key Secrets of additional model configs, which
// the definition references and never renders.
func (in *Input) CustomerActions() []render.CustomerAction {
	var out []render.CustomerAction
	for _, m := range in.Kagent.AdditionalModelConfigs {
		if m.APIKeySecret == "" {
			continue
		}
		out = append(out, render.CustomerAction{
			Action: fmt.Sprintf("create the Secret %q (key %q) in namespace kagent with the provider key of model config %q", m.APIKeySecret, m.APIKeySecretKey, m.Name),
			Why:    "the definition references the Secret and renders no value for it; the model config stays unusable until it exists"})
	}
	return out
}
