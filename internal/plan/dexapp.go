package plan

import (
	"fmt"
	"slices"

	"github.com/Masterminds/semver/v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/installations"
)

// DexAppReferencedSecrets is the first dex-app that renders every Dex client
// from a referenced Secret — next to a hand-made inline secret as well, which
// an installation enabled by hand still carries. Every client the definitions
// render is a referenced Secret (secretRef on an extra static client,
// clientSecretRef on a built-in one), so a commit onto an older dex-app would
// break Dex.
const DexAppReferencedSecrets = "3.2.2"

// DexAppRefusal says why a commit of p is refused for the dex-app on record:
// p's Dex patch declares a client with a referenced Secret and rec's dex-app
// is older than DexAppReferencedSecrets. Empty where nothing refuses — no
// referenced client, no version on record (the report carries why it could
// not be read), or a dex-app that takes them. The comparison runs either way;
// only the commit is held.
func (p Installation) DexAppRefusal(rec *installations.Record) string {
	if rec == nil || rec.DexAppVersion == "" || !p.referencesDexSecrets() {
		return ""
	}
	v, err := semver.NewVersion(rec.DexAppVersion)
	if err != nil || !v.LessThan(semver.MustParse(DexAppReferencedSecrets)) {
		return ""
	}
	return fmt.Sprintf("dex-app %s on record (%s): the referenced Dex client secrets need dex-app %s or later; pin it in %s first",
		rec.DexAppVersion, rec.DexAppSource, DexAppReferencedSecrets, installations.CollectionsKustomizationPath(p.Name))
}

// referencesDexSecrets says whether the rendered Dex patch declares a client
// whose secret is a referenced Secret.
func (p Installation) referencesDexSecrets() bool {
	return slices.ContainsFunc(p.DexClients, func(c DexClient) bool { return c.SecretRef != "" })
}
