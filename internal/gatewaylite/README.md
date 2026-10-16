# Gateway Lite Integration Boundary

`gateway-lite` mode keeps this process focused on regional API traffic:

- authenticate API keys from local cache / Redis / control-plane fallback
- acquire a regional quota lease on first use
- reserve/commit/refund spend locally in the regional Redis
- forward OpenAI / Anthropic compatible traffic
- report usage events to the control plane asynchronously

The control plane owns users, orders, total balances, ledger rows, API key
creation/revocation, and quota lease allocation.

Initial internal endpoints expected from the control plane:

- `POST /internal/key/resolve`
- `POST /internal/quota/acquire-lease`
- `POST /internal/quota/refill-lease`
- `POST /internal/usage/report`

The current first step wires `run_mode: gateway-lite` so only common and gateway
routes are registered. The next step is replacing the stock DB-backed
`APIKeyAuthMiddleware` with a middleware that uses the types and client in this
package.
