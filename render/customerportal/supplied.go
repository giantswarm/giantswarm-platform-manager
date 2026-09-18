package customerportal

// SuppliedSecretFields names the secret values the person supplies at commit
// for this input, by field: the GitHub App's client id, client secret, private
// key and webhook secret when the github plugin is on; the Sentry DSNs and
// report URI when sentry is on. A dry run passes Supplied markers for exactly
// these fields and lists them by name; no value ever appears in a dry run.
func (in *Input) SuppliedSecretFields() []string { return in.suppliedSecretFields() }

// Supplied is the marker a dry run renders in place of a supplied secret
// value: the commit step replaces it with the value the person supplies.
func Supplied(field string) string { return "SUPPLIED(" + field + ")" }

// SuppliedMarkers is the secrets map of a dry run: a marker per supplied field.
func (in *Input) SuppliedMarkers() map[string]string {
	fields := in.SuppliedSecretFields()
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		m[f] = Supplied(f)
	}
	return m
}
