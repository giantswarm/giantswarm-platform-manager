package customerportal

import "github.com/giantswarm/giantswarm-platform-manager/render"

// SuppliedSecretFields names the values the person supplies at commit for
// this input, by field: the GitHub App's id, client id, client secret,
// private key and webhook secret when the github plugin is on (the id is no
// credential, but it lives only in the encrypted file); the Sentry DSNs and
// report URI when sentry is on. A dry run passes Supplied markers for exactly
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

// MissingInputs names the required person inputs the document lacks, by
// field, sorted: each is rendered as its Missing marker. A commit refuses
// them; a comparison compares every leaf that carries one as not checked.
func (in *Input) MissingInputs() []string {
	fields := make([]string, 0, len(in.missing))
	for _, m := range in.missing {
		fields = append(fields, m.field)
	}
	return fields
}

// BuiltInDexClientID is empty for every key: the portal's client is an extra
// static client, declared whole in the dex patch; the definition adds no
// reference to a client the dex-app chart builds in.
func (in *Input) BuiltInDexClientID(string) string { return "" }

// CustomerActions is empty: every file the portal needs is a pull request of
// this manager, and the GitHub App's credentials are supplied at commit.
func (in *Input) CustomerActions() []render.CustomerAction { return nil }
