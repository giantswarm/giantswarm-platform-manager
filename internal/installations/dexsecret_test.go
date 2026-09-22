package installations

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The configs repository of the fixture installation.
const fixtureConfigs = "fleet/umbra-configs"

// encryptedDexPatch is the dex-app secret patch in the shape SOPS leaves it:
// every value ciphertext, every key and the list shape plaintext.
func encryptedDexPatch(peers, extras int, tail string) string {
	enc := func(s string) string { return "ENC[AES256_GCM,data:" + s + ",iv:fixture,tag:fixture,type:str]" }
	s := "oidc:\n    staticClients:\n        dexK8SAuthenticator:\n            clientSecret: " + enc("authenticator") + "\n            trustedPeers:"
	if peers == 0 {
		s += "\n"
	} else {
		s += "\n"
		for i := range peers {
			s += "                - " + enc("peer"+string(rune('a'+i))) + "\n"
		}
	}
	s += "        muster:\n            clientSecret: " + enc("muster") + "\n"
	if extras > 0 {
		s += "    extraStaticClients:\n"
		for i := range extras {
			s += "        - id: " + enc("id"+string(rune('a'+i))) + "\n          secret: " + enc("secret") + "\n          name: " + enc("name") + "\n"
		}
	}
	return s + tail + "sops:\n    age:\n        - recipient: age1fixture\n    lastmodified: \"2026-09-22T05:16:28Z\"\n    mac: " + enc("mac") + "\n    unencrypted_suffix: _unencrypted\n    version: 3.11.0\n"
}

// The record reads which lists the encrypted dex-app secret patch carries,
// with their entries, without decrypting anything: both lists, one of them,
// none where the lists are empty or absent, none where the patch does not
// exist; the login connectors under oidc.customer are no list of the plan's.
func TestDexSecretListsFromTheRecord(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{Configs: fixtureConfigs}}
	key := fixtureConfigs + ":" + DexSecretPatchPath(fixtureInstallation)
	cases := []struct {
		name  string
		patch string
		want  []DexSecretList
	}{
		{"both lists", encryptedDexPatch(2, 3, ""), []DexSecretList{{Path: DexTrustedPeers, Entries: 2}, {Path: DexExtraStaticClients, Entries: 3}}},
		{"the extra clients alone", encryptedDexPatch(0, 1, ""), []DexSecretList{{Path: DexExtraStaticClients, Entries: 1}}},
		{"the peers alone", encryptedDexPatch(4, 0, ""), []DexSecretList{{Path: DexTrustedPeers, Entries: 4}}},
		{"empty and absent lists", encryptedDexPatch(0, 0, ""), nil},
		{"the login connectors are no list of the plan's", encryptedDexPatch(0, 0, "    customer:\n        connectors:\n            - id: ENC[AES256_GCM,data:x,iv:x,tag:x,type:str]\n"), nil},
		{"inline secrets alone", "oidc:\n    staticClients:\n        muster:\n            clientSecret: ENC[AES256_GCM,data:x,iv:x,tag:x,type:str]\nsops:\n    version: 3.11.0\n", nil},
	}
	for _, c := range cases {
		got, err := readDexSecretLists(context.Background(), files(map[string]string{key: c.patch}), inst)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
			}
		}
	}
	got, err := readDexSecretLists(context.Background(), files(nil), inst)
	if err != nil || got != nil {
		t.Errorf("no patch on record: %+v %v", got, err)
	}
}

// A patch that cannot be read or parsed is an error naming the file and the
// repository; it is the report's, not a condition of reading the installation.
func TestDexSecretListsErrors(t *testing.T) {
	inst := Installation{Name: fixtureInstallation, Repositories: Repositories{Configs: fixtureConfigs}}
	key := fixtureConfigs + ":" + DexSecretPatchPath(fixtureInstallation)
	_, err := readDexSecretLists(context.Background(), files(map[string]string{key: "oidc: [\n"}), inst)
	if err == nil || !strings.Contains(err.Error(), DexSecretPatchPath(fixtureInstallation)) || !strings.Contains(err.Error(), fixtureConfigs) {
		t.Errorf("unparsable: %v", err)
	}
	boom := errors.New("403 as the person")
	_, err = readDexSecretLists(context.Background(), func(context.Context, string, string) (string, error) { return "", boom }, inst)
	if !errors.Is(err, boom) {
		t.Errorf("unreadable: %v", err)
	}
}
