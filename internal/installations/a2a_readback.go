package installations

import (
	"encoding/json"
	"fmt"
	"slices"
)

const a2aNameField = "name"
const a2aKeyField = "key"
const a2aClientCredentialsGrant = "client_credentials"
const a2aSecretRefField = "secretRef"

type a2aDexClient struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	GrantTypes []string `json:"grantTypes"`
	SecretRef  struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	} `json:"secretRef"`
}

func readA2AApplications(value any) ([]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var clients map[string]a2aDexClient
	if err := json.Unmarshal(raw, &clients); err != nil {
		var list []a2aDexClient
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, fmt.Errorf("A2A clients must be a list or keyed by client ID: %w", err)
		}
		clients = make(map[string]a2aDexClient, len(list))
		for _, client := range list {
			if client.ID == "" {
				return nil, fmt.Errorf("a Dex client list entry requires an ID")
			}
			if _, duplicate := clients[client.ID]; duplicate {
				return nil, fmt.Errorf("duplicate Dex client ID %q", client.ID)
			}
			clients[client.ID] = client
		}
	}
	ids := make([]string, 0, len(clients))
	for id := range clients {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	applications := make([]any, 0)
	for _, id := range ids {
		client := clients[id]
		if !slices.Contains(client.GrantTypes, a2aClientCredentialsGrant) {
			continue
		}
		if client.SecretRef.Name == "" || client.SecretRef.Key == "" {
			return nil, fmt.Errorf("application %q requires an existing Secret reference", id)
		}
		applications = append(applications, map[string]any{"id": id, a2aNameField: client.Name, a2aSecretRefField: map[string]any{a2aNameField: client.SecretRef.Name, a2aKeyField: client.SecretRef.Key}})
	}
	return applications, nil
}
