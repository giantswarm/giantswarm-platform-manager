package tools

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/giantswarm/gitops-commit/sopsenc"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// A value two installations of a wave hold is drawn once, of its declared
// shape, and written on both sides in place of its placeholder — in each
// declaration's encoding — while the encryption is left to draw the rest; a
// key pair is never shared.
func TestWaveDrawsASharedValueOnceForBothSides(t *testing.T) {
	targets := []plan.Installation{
		{Name: "hazel", GeneratedSecrets: []plan.GeneratedSecret{{Name: "pair", Kind: string(render.Base64), Length: 32, DrawnWith: "oak"}, {Name: "own", Kind: string(render.Base64), Length: 32}}},
		{Name: "oak", GeneratedSecrets: []plan.GeneratedSecret{{Name: "pair", Kind: string(render.Base64), Length: 32, DrawnWith: "hazel"}}},
	}
	values, err := drawShared(targets)
	if err != nil || len(values) != 1 || values["pair"] == "" {
		t.Fatalf("values %v, err %v", values, err)
	}
	if raw, err := base64.StdEncoding.DecodeString(values["pair"]); err != nil || len(raw) != 32 {
		t.Errorf("the pair's value is not 32 bytes in base64: %v", err)
	}
	pair := render.Generated{Name: "pair", Placeholder: render.Placeholder("pair"), Kind: render.Base64, Length: 32}
	own := render.Generated{Name: "own", Placeholder: render.Placeholder("own"), Kind: render.Base64, Length: 32}
	for _, side := range []string{"hazel", "oak"} {
		content, left := withValues([]byte("secret: "+pair.Placeholder+"\nown: "+own.Placeholder+"\n"), []render.Generated{pair, own}, values)
		if string(content) != "secret: "+values["pair"]+"\nown: "+own.Placeholder+"\n" || len(left) != 1 || left[0].Name != "own" || left[0].Kind != sopsenc.Base64 {
			t.Errorf("%s: content %q, left %+v", side, content, left)
		}
	}
	decoded := pair
	decoded.Encoding = render.Encoding(sopsenc.EncodingBase64)
	if content, left := withValues([]byte("data: "+pair.Placeholder+"\n"), []render.Generated{decoded}, values); string(content) != "data: "+base64.StdEncoding.EncodeToString([]byte(values["pair"]))+"\n" || len(left) != 0 {
		t.Errorf("a decoded leaf takes the value in base64: %q, left %+v", content, left)
	}
	alpha, err := draw(plan.GeneratedSecret{Name: "a", Kind: string(render.Alphanumeric), Length: 16, DrawnWith: "oak"})
	if err != nil || len(alpha) != 16 || strings.Trim(alpha, alphanumeric) != "" {
		t.Errorf("alphanumeric %q, err %v", alpha, err)
	}
	if _, err := drawShared([]plan.Installation{{GeneratedSecrets: []plan.GeneratedSecret{{Name: "k", Kind: string(render.KeyPairES256), DrawnWith: "oak"}}}}); err == nil || !strings.Contains(err.Error(), "k is a keypair-es256") {
		t.Errorf("a key pair is not shared: %v", err)
	}
}

// The generated values the person supplies (the peer holds them on record)
// leave the secrets the render sees and become values the commit writes, by
// name; the definition's own fields stay.
func TestSuppliedGeneratedLeavesTheRenderTheDefinitionsFields(t *testing.T) {
	p := plan.Installation{GeneratedSecrets: []plan.GeneratedSecret{{Name: "pair", Supplied: true}, {Name: "own"}}}
	secrets := map[string]string{"pair": "v", "kagent.modelKey": "k"}
	values := suppliedGenerated(p, secrets)
	if len(values) != 1 || values["pair"] != "v" || len(secrets) != 1 || secrets["kagent.modelKey"] != "k" {
		t.Errorf("values %v, secrets left %v", values, secrets)
	}
}
