# Private platform client metadata

On private installations the renderer pins klaus-gateway's client document in
Muster and Muster's client document in mcp-kubernetes. Pinned clients do not
require a metadata fetch. Public CIMD clients remain supported; the renderer
does not enable `restrictClientMetadata`.

Deploy Muster 5.64.0 or later and mcp-kubernetes 1.16.0 or later before applying
the generated pins. Existing private-address permissions remain present.
Their removal requires a separate client-coverage review and rollout.
