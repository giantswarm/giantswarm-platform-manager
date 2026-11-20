# Federation exchange connectors

The agent-platform policy selects the connector a hub uses for both its token broker and its MCP identity providers. `federation.connector.exchangeThrough` is keyed by the hub's organisation. Each entry has a `default` connector and optional `targets` overrides keyed by installation. A target without an override uses the default. An empty selection uses the connector rendered for that hub. Further hubs always use their own rendered connector.

```yaml
federation:
  connector:
    exchangeThrough:
      giantswarm:
        default: giantswarm-simple-oidc
        targets:
          burrow: ""
```

Here the first hub exchanges into `burrow` through its rendered connector. Other targets keep the default. The broker still exchanges API tokens into a browse-only target; that target has no MCP identity provider.

A Dex connector ID is part of its subjects. Before selecting a different connector for a target:

1. Verify the target's browser-capable connector, its referenced secret and the hub's login client are deployed. Check the authorization redirect presents the target's registered client ID.
2. Inventory the target's sessions and grants without reading token values. Subject-scoped grants belong to the issuer and raw subject. A changed connector requires new authorization unless an independently verified identity migration exists. Matching email addresses alone does not establish that identity.
3. Check the portal resolves the connector to the intended catalog user. Check token caches and refresh sessions cannot keep issuing the old subject after the selection changes.
4. Change the one target's policy entry and reconcile the hub. Update that target's browser identity-check connector list through its owning template or definition, and retain issuer and raw-subject equality.
5. Verify an authenticated portal request and a downstream browser connection with each supported staff login method before selecting another target.

Removing an override restores the default for subsequent exchanges. Tokens, sessions and grants issued under either subject retain their own lifetime; a policy rollback does not migrate or revoke them. Keep the old connector and credentials until no consumer uses them. Remove the default override only when every target is ready to use its rendered connector.
