package plan

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// ConfigurationDigest hashes a JSON-compatible configuration value, preserving
// list order while ignoring map order. Only the digest belongs on an Action.
func ConfigurationDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode configuration for digest: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded)), nil
}

// TrustedIssuerSnapshot captures the complete merged issuer list, including
// omitted permission flags, without storing configuration values on the Action.
func TrustedIssuerSnapshot(content string) ([]Kept, error) {
	_, root, err := mapping([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("read trusted issuers: %w", err)
	}
	issuers := at(root, strings.Split(ListTrustedIssuers, ".")...)
	if issuers == nil || issuers.Tag == tagNull {
		return nil, nil
	}
	var value any
	if err := issuers.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode trusted issuers: %w", err)
	}
	digest, err := ConfigurationDigest(value)
	if err != nil {
		return nil, err
	}
	return []Kept{{List: ListTrustedIssuers, Digest: digest}}, nil
}
