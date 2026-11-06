package agentplatform

import "github.com/giantswarm/giantswarm-platform-manager/render"

// SuppliedSecretFields names the secret values the person supplies at commit
// for this installation, by field: the Slack app's credentials where the
// gateway runs, the chat's Anthropic API key where the portal runs the chat,
// nothing else. A dry run passes Supplied markers for exactly these fields
// and lists them by name; no value ever appears in a dry run.
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
// pull requests, wherever kagent runs: a model with its key, the step the
// model-key action (actions) holds up live until some ModelConfig in kagent's
// namespace reads its model key resolved (modelConfigCondition), and, on the
// 4 line, every Agent off kagent's own default-model-config, which the
// agent-models action holds up until none names it.
func (in *Input) CustomerActions() []render.CustomerAction {
	if !in.kagent() {
		return nil
	}
	actions := []render.CustomerAction{{Action: in.modelKeyNote(), Why: modelKeyWhy, Dimension: modelKeyDimension}}
	if in.Installation.ChartLine == lineFour {
		actions = append(actions, render.CustomerAction{Action: agentModelsNote(), Why: agentModelsWhy, Dimension: agentModelsDimension})
	}
	return actions
}
