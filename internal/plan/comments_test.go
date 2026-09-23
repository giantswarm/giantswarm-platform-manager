package plan

import (
	"strings"
	"testing"
)

// A file written back from its own nodes is the file: a comment after a
// nested block at the indentation of an outer key — the notes at the end of
// muster's oauth.server section, after its trustedIssuers — stays at that
// key's indentation, one at the inner key's own stays there, and one at the
// top level after the last block stays at the top level.
func TestMappingKeepsFootCommentsWhereTheyStand(t *testing.T) {
	const glean = `muster:
  muster:
    oauth:
      mcpClient:
        postLoginRedirectAllowlist:
          - https://portal.example.test/
      server:
        trustedAudiences:
          - kagent
        trustedIssuers:
          - issuer: https://dex.example.test
            allowedAudiences:
              - kagent
              # the portal's own client
              - backstage
            subjectClaim: email
        # No tokenExchangeBroker here: the broker is for the Dev Portal only,
        # and this installation has none.
gateway:
  jwksEgress:
    enabled: true
# kept at the end of the file
`
	inner := strings.Replace(glean, "            subjectClaim: email\n", "            subjectClaim: email\n            # a note on the issuer's entry\n", 1)
	for name, file := range map[string]string{"after the nested block": glean, "after an inner note": inner} {
		doc, _, err := mapping([]byte(file))
		if err != nil {
			t.Fatal(err)
		}
		out, err := encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		if string(out) != file {
			t.Errorf("%s, written back:\n%s\nwant\n%s", name, out, file)
		}
	}
}
