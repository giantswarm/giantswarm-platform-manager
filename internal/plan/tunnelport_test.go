package plan

import (
	"errors"
	"strings"
	"testing"
)

// renderedTunnelport is a hub's entries as the agent-platform definition
// renders them: the hub gopher as a consumer, its trust-bundle token, one
// tunnel to burrow's Dex.
const renderedTunnelport = `# The hub gopher's entries of teleport-fleet's tunnelport values.
tunnelport:
  consumers:
    gopher:
      installNamespace: agent-platform
      issuer: https://irsa.gopher.example.io
  trustBundle:
    tokens:
      - name: tunnelport-trust-bundle-token-gopher
        consumer: gopher
  tunnels:
    - name: dex-burrow
      appLabels:
        app: dex
        cluster: burrow
        customer: giantswarm
      tokens:
        - name: dex-burrow-bot-token
          consumer: gopher
`

// currentTunnelport is the fleet's values file: the template's own values,
// another hub's consumer and tunnel, the trust bundle's tokens, a stale entry
// of gopher's, a comment.
const currentTunnelport = `# Tunnelport resources on Teleport Central, rendered by templates/tunnelport.yaml.
tunnelport:
  dnsSans:
    - '{{ join.kubernetes.service_account.name }}.{{ join.kubernetes.service_account.namespace }}.svc'
  consumers:
    gopher:
      installNamespace: stale-namespace
      jwks: '{"keys":[]}'
    otter:
      installNamespace: giantswarm
      jwks: '{"keys":[]}'
  trustBundle:
    tokens:
      - name: tunnelport-trust-bundle-token-otter
        consumer: otter
  tunnels:
    # otter reaches gopher's Dex
    - name: dex-gopher
      appLabels:
        app: dex
        cluster: gopher
        customer: giantswarm
      tokens:
        - name: dex-gopher-bot-token-otter
          consumer: otter
          sharedWith: [weasel]
other:
  key: value
`

func TestKeepTunnelportValuesEditsTheHubsEntriesInAndKeepsTheRest(t *testing.T) {
	got, kept, err := keepTunnelportValues([]byte(renderedTunnelport), []byte(currentTunnelport))
	if err != nil {
		t.Fatal(err)
	}
	want := []Kept{{listConsumers, "otter"}, {listTrustBundleTokens, "tunnelport-trust-bundle-token-otter"}, {listTunnels, "dex-gopher"}}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Errorf("kept[%d] = %v, want %v", i, kept[i], want[i])
		}
	}
	s := string(got)
	for _, frag := range []string{
		"# Tunnelport resources on Teleport Central",
		"  dnsSans:\n    - '{{ join.kubernetes.service_account.name }}.{{ join.kubernetes.service_account.namespace }}.svc'\n",
		"    gopher:\n      installNamespace: agent-platform\n      issuer: https://irsa.gopher.example.io\n    otter:\n",
		"      - name: tunnelport-trust-bundle-token-otter\n        consumer: otter\n      - name: tunnelport-trust-bundle-token-gopher\n        consumer: gopher\n",
		"    # otter reaches gopher's Dex\n    - name: dex-gopher\n",
		"          consumer: otter\n          sharedWith: [weasel]\n",
		"    - name: dex-burrow\n      appLabels:\n        app: dex\n        cluster: burrow\n        customer: giantswarm\n",
		"other:\n  key: value\n",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("edited file lacks %q:\n%s", frag, s)
		}
	}
	for _, gone := range []string{"stale-namespace", "The hub gopher's entries"} {
		if strings.Contains(s, gone) {
			t.Errorf("edited file carries %q:\n%s", gone, s)
		}
	}
	if strings.Index(s, "name: dex-gopher") > strings.Index(s, "name: dex-burrow") {
		t.Errorf("the hub's new tunnel is not appended after the others:\n%s", s)
	}
}

func TestKeepTunnelportValuesLeavesTheFileAsItIsWhenEveryEntryIsThere(t *testing.T) {
	edited, _, err := keepTunnelportValues([]byte(renderedTunnelport), []byte(currentTunnelport))
	if err != nil {
		t.Fatal(err)
	}
	// The same entries again, with a comment on one of them and another key order: no edit.
	current := strings.Replace(string(edited), "    - name: dex-burrow\n      appLabels:\n        app: dex\n        cluster: burrow\n",
		"    # gopher reaches burrow's Dex\n    - name: dex-burrow\n      appLabels:\n        cluster: burrow\n        app: dex\n", 1)
	got, kept, err := keepTunnelportValues([]byte(renderedTunnelport), []byte(current))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != current {
		t.Errorf("an unchanged file was rewritten:\n--- current\n%s\n--- got\n%s", current, got)
	}
	if len(kept) != 3 {
		t.Errorf("kept %v, want the three entries of otter", kept)
	}
}

func TestKeepTunnelportValuesCreatesTheListsUnderAnEmptyTunnelport(t *testing.T) {
	got, kept, err := keepTunnelportValues([]byte(renderedTunnelport), []byte("tunnelport: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if kept != nil {
		t.Errorf("kept %v, want nothing", kept)
	}
	if !strings.Contains(string(got), "tunnelport:\n  consumers:\n    gopher:\n") || !strings.Contains(string(got), "  tunnels:\n    - name: dex-burrow\n") {
		t.Errorf("lists not created in block style:\n%s", got)
	}
}

func TestKeepTunnelportValuesRefusesAFileWithoutTunnelportValues(t *testing.T) {
	for _, current := range []string{"", "other:\n  key: value\n", "tunnelport: 3\n"} {
		_, _, err := keepTunnelportValues([]byte(renderedTunnelport), []byte(current))
		if !errors.Is(err, errNoTunnelport) && !errors.Is(err, errNoMapping) {
			t.Errorf("%q: err = %v, want errNoTunnelport or errNoMapping", current, err)
		}
	}
}

func TestKeepTunnelportValuesRefusesAnEntryWithoutAName(t *testing.T) {
	_, _, err := keepTunnelportValues([]byte(renderedTunnelport), []byte("tunnelport:\n  tunnels:\n    - appLabels: {app: dex}\n"))
	if err == nil || !strings.Contains(err.Error(), "without a name") {
		t.Errorf("err = %v, want an entry without a name", err)
	}
}

// sharedTunnel is the fleet's values with a tunnel two hubs reach: gopher's
// token and the tunnel's labels on record with stale content, otter's token
// beside them.
const sharedTunnel = `tunnelport:
  consumers:
    gopher:
      installNamespace: agent-platform
      issuer: https://irsa.gopher.example.io
  trustBundle:
    tokens:
      - name: tunnelport-trust-bundle-token-gopher
        consumer: gopher
  tunnels:
    - name: dex-burrow
      appLabels:
        app: dex
        cluster: burrow
        customer: stale
      tokens:
        - name: dex-burrow-bot-token
          consumer: stale
        # otter is burrow's second hub
        - name: dex-burrow-bot-token-otter
          consumer: otter
`

func TestKeepTunnelportValuesKeepsTheOtherHubsTokensOfASharedTunnel(t *testing.T) {
	got, kept, err := keepTunnelportValues([]byte(renderedTunnelport), []byte(sharedTunnel))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Kept{{listTunnelTokens("dex-burrow"), "dex-burrow-bot-token-otter"}}; len(kept) != 1 || kept[0] != want[0] {
		t.Errorf("kept %v, want %v", kept, want)
	}
	s := string(got)
	for _, frag := range []string{
		"    - name: dex-burrow\n      appLabels:\n        app: dex\n        cluster: burrow\n        customer: giantswarm\n      tokens:\n",
		"        - name: dex-burrow-bot-token\n          consumer: gopher\n",
		"        # otter is burrow's second hub\n        - name: dex-burrow-bot-token-otter\n          consumer: otter\n",
	} {
		if !strings.Contains(s, frag) {
			t.Errorf("edited file lacks %q:\n%s", frag, s)
		}
	}
	if strings.Contains(s, "stale") {
		t.Errorf("edited file carries the stale content:\n%s", s)
	}
	if strings.Index(s, "name: dex-burrow-bot-token\n") > strings.Index(s, "name: dex-burrow-bot-token-otter\n") {
		t.Errorf("the hub's token left its place:\n%s", s)
	}
	// The edited file again: nothing to change, byte for byte, otter's token still kept.
	again, kept, err := keepTunnelportValues([]byte(renderedTunnelport), got)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != s {
		t.Errorf("an unchanged file was rewritten:\n--- current\n%s\n--- got\n%s", s, again)
	}
	if len(kept) != 1 || kept[0].Entry != "dex-burrow-bot-token-otter" {
		t.Errorf("kept %v, want otter's token", kept)
	}
}
