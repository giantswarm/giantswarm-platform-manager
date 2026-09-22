package installations

import (
	"context"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/giantswarm/giantswarm-platform-manager/internal/gh"
)

// DexSecretPatchPath is where the installation's configs repository keeps the
// dex-app secret patch: SOPS-encrypted values whose keys stay plaintext — the
// hand-registered clients with their inline secrets, the authenticator's
// trusted peers, the login connectors. The engine never decrypts it and never
// writes it; it reads which lists it carries.
func DexSecretPatchPath(name string) string {
	return "installations/" + name + "/apps/dex-app/secret-values.yaml.patch"
}

// The lists the definitions render into the plaintext dex-app patch that the
// values merge takes whole from the encrypted patch where it carries them:
// app-operator merges the config ConfigMap and the config Secret, and
// helm-controller its valuesFrom, by replacing a list, never by appending.
const (
	DexExtraStaticClients = "oidc.extraStaticClients"
	DexTrustedPeers       = "oidc.staticClients.dexK8SAuthenticator.trustedPeers"
)

// DexSecretList is one of those lists as the encrypted dex-app secret patch
// on record carries it: its path and how many entries it has.
type DexSecretList struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
}

// readDexSecretLists reads which of the lists inst's encrypted dex-app secret
// patch carries, with how many entries, as the person: the keys and the list
// shape of a SOPS-encrypted file are plaintext, so nothing is decrypted. No
// patch, or one without the lists, is none, no error; a patch that cannot be
// read or parsed is an error of the report. An empty list counts as none: the
// merge takes an empty list from neither side.
func readDexSecretLists(ctx context.Context, read Reader, inst Installation) ([]DexSecretList, error) {
	path := DexSecretPatchPath(inst.Name)
	data, err := read(ctx, inst.Repositories.Configs, path)
	if errors.Is(err, gh.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the encrypted Dex values on record: %w", err)
	}
	lists, err := dexSecretLists(data)
	if err != nil {
		return nil, fmt.Errorf("the encrypted Dex values on record: %s in %s: %w", path, inst.Repositories.Configs, err)
	}
	return lists, nil
}

// dexSecretLists reads the lists of the encrypted patch's document, in the
// order the plaintext patch renders them.
func dexSecretLists(patch string) ([]DexSecretList, error) {
	var doc struct {
		OIDC struct {
			StaticClients struct {
				DexK8SAuthenticator struct {
					TrustedPeers []yaml.Node `yaml:"trustedPeers"`
				} `yaml:"dexK8SAuthenticator"`
			} `yaml:"staticClients"`
			ExtraStaticClients []yaml.Node `yaml:"extraStaticClients"`
		} `yaml:"oidc"`
	}
	if err := yaml.Unmarshal([]byte(patch), &doc); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	var out []DexSecretList
	if n := len(doc.OIDC.StaticClients.DexK8SAuthenticator.TrustedPeers); n > 0 {
		out = append(out, DexSecretList{Path: DexTrustedPeers, Entries: n})
	}
	if n := len(doc.OIDC.ExtraStaticClients); n > 0 {
		out = append(out, DexSecretList{Path: DexExtraStaticClients, Entries: n})
	}
	return out, nil
}
