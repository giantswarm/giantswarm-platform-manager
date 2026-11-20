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

`federation.identityCheckConnectors` selects muster's complete browser identity-check connector list per installation. An absent installation keeps its local override or shared defaults. An explicit empty list clears the additional connectors. The setting preserves the installation's login connector pin and private-address permissions. Every staff hub's selected exchange connector must appear in the list; a mismatched selection is refused.

```yaml
federation:
  identityCheckConnectors:
    burrow: [giantswarm-ad, giantswarm-gopher-oidc]
  connector:
    exchangeThrough:
      giantswarm:
        default: giantswarm-simple-oidc
        targets:
          burrow: ""
```

This selects both sides for a target whose staff hub is `gopher`. Keep every supported customer browser connector in a customer installation's list. Token-exchange-only connectors cannot authenticate a browser and must not be included in the cutover list.

A Dex connector ID is part of its subjects. Before selecting a different connector for a target:

1. Verify the target's browser-capable connector, its referenced secret and the hub's login client are deployed. Check the authorization redirect presents the target's registered client ID.
2. Inventory the target's sessions and grants without reading token values. Subject-scoped grants belong to the issuer and raw subject. A changed connector requires new authorization unless an independently verified identity migration exists. Matching email addresses alone does not establish that identity.
3. Check the portal resolves the connector to the intended catalog user. Check token caches and refresh sessions cannot keep issuing the old subject after the selection changes.
4. Select the target in both policy maps and reconcile the hub and target together in one mixed-capability wave, including any new login credential holders. Retain issuer and raw-subject equality.
5. Verify an authenticated portal request and a downstream browser connection with each supported staff login method before selecting another target.

Removing an exchange override restores the default for subsequent exchanges. Roll back the target's identity-check list explicitly alongside it; removing its identity-check policy entry preserves the list already on record. Tokens, sessions and grants issued under either subject retain their own lifetime; a policy rollback does not migrate or revoke them. Keep the old connector and credentials until no consumer uses them. Remove the default override only when every target is ready to use its rendered connector.

The rollout watch verifies the complete trusted-issuer list from the merged values, including locally preserved settings and omitted permission flags. Actions record its SHA-256 digest alongside the audience identifiers used for live verification. Added issuers, changed token types and added private-address permissions are drift even when the shared definition does not name them.

For an action without that snapshot, `watch_action` reads its installation's values file from the merged PR as the caller and records the digest in status. A shared PR is matched by the installation's exact path. If the action did not change its values file, the watch reads the recorded marker on the default branch. `verify_installation` reuses these records without reading GitHub.
