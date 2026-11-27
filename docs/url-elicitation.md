# URL elicitation for live reads

Live tools relay URL interactions requested by Muster using mcp-toolkit's
`urlelicitation` and mcp-oauth's caller-bound request-state sealer. They retain
the producer's message and URL; a URL interaction is not necessarily sign-in.

On MCP 2026-07-28, a client declaring URL elicitation receives `input_required`
with sealed `requestState`. On 2025-11-25 it receives JSON-RPC `-32042`.
The configured request-state key is required to enable the relay.

Modern retry state is verified before handlers run, even if the retry drops
URL capability or its forwarded identity. Decline and cancel run nothing.
An accepted retry answers the same Muster read, once. Accept means consent to
open the URL; the producer still checks whether the interaction completed.
A supplied state without an answer keeps the pending interaction, so
`merge_action` and `watch_action` cannot perform effects around that prompt.
Sealing failures fail the call instead of returning incomplete checks.

The protocol contracts are [MCP multi round-trip requests](https://modelcontextprotocol.io/specification/2026-07-28/basic/patterns/mrtr)
and [URL elicitation](https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation).

This relay does not replace endpoint authentication or per-operation
authorization. The existing private identity-forwarding paths need the
[per-backend access-token migration](https://github.com/giantswarm/giantswarm/issues/39102)
for standard MCP OAuth resource-server enforcement.
