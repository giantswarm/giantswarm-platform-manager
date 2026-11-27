package installations

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	a2aFixtureGrantTypes  = "grantTypes"
	a2aFixtureApplication = "application"
	a2aFixtureClientName  = "Application"
	a2aFixtureStaff       = "staff"
	a2aFixtureSecret      = "dex-application"
	a2aFixtureSecretKey   = "secret"
)

func TestReadA2AApplications(t *testing.T) {
	applications, err := readA2AApplications(map[string]any{
		a2aFixtureStaff:       map[string]any{a2aFixtureGrantTypes: []string{"authorization_code"}},
		a2aFixtureApplication: map[string]any{a2aNameField: a2aFixtureClientName, a2aFixtureGrantTypes: []string{a2aClientCredentialsGrant}, a2aSecretRefField: map[string]any{a2aNameField: a2aFixtureSecret, a2aKeyField: a2aFixtureSecretKey}},
	})
	require.NoError(t, err)
	require.Equal(t, []any{map[string]any{"id": a2aFixtureApplication, a2aNameField: a2aFixtureClientName, a2aSecretRefField: map[string]any{a2aNameField: a2aFixtureSecret, a2aKeyField: a2aFixtureSecretKey}}}, applications)
	_, err = readA2AApplications(map[string]any{a2aFixtureApplication: map[string]any{a2aFixtureGrantTypes: []string{a2aClientCredentialsGrant}}})
	require.ErrorContains(t, err, "existing Secret reference")
}

func TestReadA2AApplicationClientFormats(t *testing.T) {
	client := map[string]any{"id": a2aFixtureApplication, a2aNameField: a2aFixtureClientName, a2aFixtureGrantTypes: []string{a2aClientCredentialsGrant}, a2aSecretRefField: map[string]any{a2aNameField: a2aFixtureSecret, a2aKeyField: a2aFixtureSecretKey}}
	expected, err := readA2AApplications(map[string]any{a2aFixtureApplication: client})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		value any
	}{
		{"map", map[string]any{a2aFixtureApplication: client}},
		{"list", []any{client, map[string]any{"id": a2aFixtureStaff}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, err := readA2AApplications(test.value)
			require.NoError(t, err)
			require.Equal(t, expected, actual)
		})
	}
	for _, value := range []any{nil, []any{}, map[string]any{}, []any{map[string]any{"id": a2aFixtureStaff}}} {
		actual, err := readA2AApplications(value)
		require.NoError(t, err)
		require.Empty(t, actual)
	}
	for _, test := range []struct {
		name  string
		value any
		err   string
	}{
		{"missing ID", []any{map[string]any{}}, "requires an ID"},
		{"duplicate ID", []any{client, client}, "duplicate Dex client ID"},
		{"missing reference", []any{map[string]any{"id": a2aFixtureApplication, a2aFixtureGrantTypes: []string{a2aClientCredentialsGrant}}}, "existing Secret reference"},
		{"invalid shape", true, "must be a list or keyed by client ID"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := readA2AApplications(test.value)
			require.ErrorContains(t, err, test.err)
		})
	}
}
