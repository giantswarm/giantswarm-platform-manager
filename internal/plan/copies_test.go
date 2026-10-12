package plan

import (
	"slices"
	"strings"
	"testing"
)

// The pair's peer: the hub's credentials file for the installation; and the
// key path of the Valkey password in a server's credentials Secret.
const (
	peerFile       = "acme/mcs:extras/hub/secrets/x-token-exchange-credentials.yaml" // #nosec G101 -- a file path, not a value
	valkeyPassword = "stringData.VALKEY_PASSWORD"                                    // #nosec G101 -- a key path, not a value
)

// A target of a wave whose record holds unrelated generated values: the
// server's credentials Secret on a 3-line record and its Valkey Secret, the
// 4 line adding the Dex client Secret and the Valkey password key — two
// carries of the installation's own (the client secret into the new file,
// the password into the credentials file) — and the pair's value this plan
// supplies at commit from the hub's file. The wave's vault copy selects the
// pair alone, lists the two carries outside it, and the carry refusal names
// them as not the wave's to copy, with the installation's own reconcile as
// the way out; without the wave's selection, the same plan refuses the two
// carries as its own.
func TestWaveVaultCopiesSelectThePairAlone(t *testing.T) {
	g := generatedOf(client, key, password, pair)
	frozen(g, []holder{
		{file: credentials, change: ChangeUpdate, secret: []string{client, key, password}, lacks: []string{password}, fillable: true,
			paths: map[string]string{client: "stringData.DEX_CLIENT_SECRET", key: "stringData.MCP_OAUTH_ENCRYPTION_KEY", password: valkeyPassword}}, // #nosec G101 -- key paths, not values
		{file: valkey, change: ChangeUnchanged, secret: []string{password}, paths: map[string]string{password: defaultKey}},
		{file: dexClient, change: ChangeCreate, secret: []string{client}, paths: map[string]string{client: secretKey}},
	}, nil, true, nil)
	g[pair].Peer, g[pair].Supplied = peerFile, true
	p := Installation{Name: "x", GeneratedSecrets: []GeneratedSecret{*g[client], *g[key], *g[password], *g[pair]}}

	own := p.CarryRefusal()
	for _, want := range []string{client + " is on record in " + credentials, password + " is on record in " + valkey} {
		if !strings.Contains(own, want) {
			t.Errorf("the installation's own carry refusal lacks %q: %q", want, own)
		}
	}
	if strings.Contains(own, "wave") {
		t.Errorf("the installation's own carry refusal speaks of a wave: %q", own)
	}

	copies := p.WaveVaultCopies()
	want := []VaultCopy{
		{Name: pair, From: peerFile, To: "--secret " + pair + "=… at commit"},
		{Name: client, From: keyPath(credentials, "DEX_CLIENT_SECRET"), To: keyPath(dexClient, "secret"), Outside: true},
		{Name: password, From: keyPath(valkey, "default"), To: keyPath(credentials, "VALKEY_PASSWORD"), Outside: true},
	}
	if !slices.Equal(copies, want) {
		t.Fatalf("vault copies %+v\nwant %+v", copies, want)
	}
	p.VaultCopies = copies
	wave := p.CarryRefusal()
	for _, want := range []string{"the wave's vault copy selects the pair it moves (" + pair + ")", client + " (" + keyPath(dexClient, "secret") + " takes it from " + keyPath(credentials, "DEX_CLIENT_SECRET") + ")", password + " (" + keyPath(credentials, "VALKEY_PASSWORD"), "outside it: not copied by the wave", "reconcile x alone first"} {
		if !strings.Contains(wave, want) {
			t.Errorf("the wave's carry refusal lacks %q: %q", want, wave)
		}
	}
	if strings.Contains(wave, client+" is on record in") || strings.Contains(wave, password+" is on record in") {
		t.Errorf("the wave's carry refusal carries the installation's own clauses: %q", wave)
	}
}

// A plan that moves no side of a pair has no wave vault copy: its carries
// are its own reconcile's, over a set as alone. A pair kept on both sides
// moves nothing either.
func TestWaveVaultCopiesNoneWithoutAPairMoved(t *testing.T) {
	carry := Carry{From: keyPath(credentials, "DEX_CLIENT_SECRET"), To: keyPath(dexClient, "secret"), Create: true}
	own := GeneratedSecret{Name: client, Kept: true, FrozenIn: []string{credentials}, Carries: []Carry{carry}}
	for _, p := range []Installation{
		{Name: "x", GeneratedSecrets: []GeneratedSecret{own}},
		{Name: "x", GeneratedSecrets: []GeneratedSecret{own, {Name: pair, Peer: peerFile, Kept: true, FrozenIn: []string{"acme/mcs:extras/pair.yaml"}}}},
	} {
		if copies := p.WaveVaultCopies(); copies != nil {
			t.Errorf("vault copies %+v, want none", copies)
		}
		if r := p.CarryRefusal(); !strings.Contains(r, client+" is on record in "+credentials) || strings.Contains(r, "wave") {
			t.Errorf("carry refusal %q", r)
		}
	}
}

// A pair both sides of which the wave creates is drawn once for both:
// nothing is copied for it, and the wave's vault copy lists the carries
// outside the pair alone, refused by name; a pair carried into a file that
// lacks it is copied at its key paths.
func TestWaveVaultCopiesDrawnOnceOrCarried(t *testing.T) {
	outside := GeneratedSecret{Name: password, Kept: true, FrozenIn: []string{valkey}, Carries: []Carry{{From: keyPath(valkey, "default"), To: keyPath(credentials, "VALKEY_PASSWORD")}}}
	drawn := Installation{Name: "x", GeneratedSecrets: []GeneratedSecret{outside, {Name: pair, Peer: peerFile, DrawnWith: "hub"}}}
	copies := drawn.WaveVaultCopies()
	if !slices.Equal(copies, []VaultCopy{{Name: password, From: keyPath(valkey, "default"), To: keyPath(credentials, "VALKEY_PASSWORD"), Outside: true}}) {
		t.Fatalf("vault copies %+v", copies)
	}
	drawn.VaultCopies = copies
	if r := drawn.CarryRefusal(); !strings.Contains(r, "("+pair+")") || !strings.Contains(r, password+" (") || strings.Contains(r, password+" is on record") {
		t.Errorf("carry refusal %q", r)
	}
	carried := Installation{Name: "x", GeneratedSecrets: []GeneratedSecret{{Name: pair, Peer: peerFile, Kept: true, FrozenIn: []string{"acme/mcs:extras/pair.yaml"}, Carries: []Carry{{From: "acme/mcs:extras/pair.yaml#stringData.secret", To: "acme/mcs:extras/copy.yaml#stringData.secret", Create: true}}}}}
	if copies := carried.WaveVaultCopies(); !slices.Equal(copies, []VaultCopy{{Name: pair, From: "acme/mcs:extras/pair.yaml#stringData.secret", To: "acme/mcs:extras/copy.yaml#stringData.secret"}}) {
		t.Fatalf("vault copies %+v", copies)
	}
	carried.VaultCopies = carried.WaveVaultCopies()
	if r := carried.CarryRefusal(); !strings.Contains(r, pair+" is on record in") || strings.Contains(r, "outside") {
		t.Errorf("carry refusal %q", r)
	}
}
