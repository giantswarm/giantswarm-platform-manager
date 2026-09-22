package render

import "testing"

func TestEncodedDeclarationSharesTheNameNotThePlaceholder(t *testing.T) {
	raw := Generated{Name: "maple-backstage-dex-client-secret", Placeholder: Placeholder("maple-backstage-dex-client-secret"), Kind: Base64, Length: 32}
	encoded := raw.Encoded(EncodedBase64)
	if encoded.Name != raw.Name || encoded.Kind != raw.Kind || encoded.Length != raw.Length {
		t.Fatalf("the encoded declaration changed the value's name or shape: %+v", encoded)
	}
	if encoded.Encoding != EncodedBase64 || raw.Encoding != "" {
		t.Fatalf("encodings: raw %q, encoded %q", raw.Encoding, encoded.Encoding)
	}
	if want := "GENERATED(maple-backstage-dex-client-secret.base64)"; encoded.Placeholder != want {
		t.Fatalf("encoded placeholder %q, want %q", encoded.Placeholder, want)
	}
	if encoded.Placeholder == raw.Placeholder {
		t.Fatal("the raw and the encoded declaration share a placeholder")
	}
}

func TestIsMarker(t *testing.T) {
	for value, want := range map[string]bool{
		Placeholder("x"): true,
		Generated{Name: "x"}.Encoded(EncodedBase64).Placeholder: true,
		Supplied("plugins.github.clientId"):                     true,
		Missing("portal.organization"):                          true,
		"backstage":                                             false,
		"R0VORVJBVEVEKHgp":                                      false,
		"a GENERATED(x) inside a sentence":                      false,
		"":                                                      false,
	} {
		if got := IsMarker(value); got != want {
			t.Errorf("IsMarker(%q) = %v, want %v", value, got, want)
		}
	}
}
