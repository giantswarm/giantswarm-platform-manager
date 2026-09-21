package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// SuppliedSecretFields names the secret values the person supplies at commit
// for this installation, by field: the Slack app's credentials where the
// gateway runs, nothing else. A dry run passes Supplied markers for exactly
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

// MissingInputs is empty: every choice of this definition has a default, so
// no required person input is ever absent from the document.
func (in *Input) MissingInputs() []string { return nil }

// CustomerActions names what the rollout needs from the person beyond the
// pull requests: the model key Secret, wherever kagent runs — the step the
// runtime feature's model-key action (actions) holds up live until the default
// ModelConfig is Accepted.
func (in *Input) CustomerActions() []render.CustomerAction {
	if !in.kagent() {
		return nil
	}
	return []render.CustomerAction{{Action: modelKeyNote, Why: modelKeyWhy}}
}
