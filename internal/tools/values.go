package tools

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"

	"github.com/giantswarm/gitops-commit/sopsenc"

	"github.com/giantswarm/giantswarm-platform-manager/internal/plan"
	"github.com/giantswarm/giantswarm-platform-manager/render"
)

// The values the commit holds for generated names, by name, before the
// encryption draws the rest: a wave's shared draws (drawShared) and the
// generated values the person supplied (suppliedGenerated). Each lands in
// place of its placeholder (withValues) and is drawn by nothing else.

// drawShared draws, once for the wave, every generated value two
// installations of it hold (plan.GeneratedSecret's DrawnWith): both sides
// are new, and one draw written to both is what keeps them one value. A
// name is drawn once whatever the plans that list it.
func drawShared(targets []plan.Installation) (map[string]string, error) {
	values := map[string]string{}
	for _, p := range targets {
		for _, g := range p.GeneratedSecrets {
			if g.DrawnWith == "" {
				continue
			}
			if _, ok := values[g.Name]; ok {
				continue
			}
			v, err := draw(g)
			if err != nil {
				return nil, err
			}
			values[g.Name] = v
		}
	}
	return values, nil
}

// alphanumeric is the alphabet of an alphanumeric value, as the encryption draws one.
const alphanumeric = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// draw draws a value of g's shape from crypto/rand, as the encryption would:
// Length random bytes in standard base64, or Length alphanumeric characters.
// A key pair is drawn by the encryption alone, in one installation's files.
func draw(g plan.GeneratedSecret) (string, error) {
	switch render.GeneratedKind(g.Kind) {
	case render.Base64:
		buf := make([]byte, g.Length)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("drawing %s: %w", g.Name, err)
		}
		return base64.StdEncoding.EncodeToString(buf), nil
	case render.Alphanumeric:
		out := make([]byte, g.Length)
		for i := range out {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphanumeric))))
			if err != nil {
				return "", fmt.Errorf("drawing %s: %w", g.Name, err)
			}
			out[i] = alphanumeric[n.Int64()]
		}
		return string(out), nil
	}
	return "", fmt.Errorf("%s is a %s: a value two installations share is drawn once as base64 or alphanumeric only", g.Name, g.Kind)
}

// suppliedGenerated takes the values of the generated names the plan asks
// the person for (plan.GeneratedSecret's Supplied: the peer holds the value
// on record) out of secrets, by name. The commit writes each in place of
// its placeholder, and the render sees the definition's own fields alone.
func suppliedGenerated(p plan.Installation, secrets map[string]string) map[string]string {
	values := map[string]string{}
	for _, g := range p.GeneratedSecrets {
		if g.Supplied {
			values[g.Name] = secrets[g.Name]
			delete(secrets, g.Name)
		}
	}
	return values
}

// withValues writes the values the commit holds in place of their
// placeholders, each in its declaration's encoding, and answers the content
// with the declarations left for the encryption to draw: a declaration whose
// value is known is left out, so nothing draws it again.
func withValues(content []byte, generated []render.Generated, values map[string]string) ([]byte, []sopsenc.Generated) {
	var left []sopsenc.Generated
	for _, g := range generated {
		if v, ok := values[g.Name]; ok {
			content = bytes.ReplaceAll(content, []byte(g.Placeholder), []byte(encoded(v, g.Encoding)))
			continue
		}
		left = append(left, sopsenc.Generated{Name: g.Name, Placeholder: g.Placeholder, Kind: sopsenc.Kind(g.Kind), Length: g.Length, Half: sopsenc.Half(g.Half), Encoding: sopsenc.Encoding(g.Encoding)})
	}
	return content, left
}

// encoded is v as a declaration receives it: as it is, or base64 where the
// consumer decodes the leaf.
func encoded(v string, encoding render.Encoding) string {
	if sopsenc.Encoding(encoding) == sopsenc.EncodingBase64 {
		return base64.StdEncoding.EncodeToString([]byte(v))
	}
	return v
}
