# Pinned Convex contracts for M0

Retrieved **2026-09-09**. These are unmodified upstream OpenAPI snapshots, not evidence of a successful live integration. Retain their embedded license notices. Updating a snapshot requires reviewing the probe wire types and checksum assertions in `tests/m0/safety_test.go`.

| File | Origin | SHA-256 |
|---|---|---|
| `management-openapi.json` | https://api.convex.dev/v1/openapi.json | `8107cc743e0b297c42262ec40374b44294f9fbe959d0e61ba9b7beb8237627da` |
| `deployment-openapi.json` | [convex-backend at fd93e24468fa755237a38936a7aa292c2ac88601](https://raw.githubusercontent.com/get-convex/convex-backend/fd93e24468fa755237a38936a7aa292c2ac88601/npm-packages/@convex-dev/platform/deployment-openapi.json) | `b4e1df4dfdb02dbfb401a623feaa6339495c63f88c24acb7dc5b69603a66fa3d` |

## Reviewed probe surface

Management base: `https://api.convex.dev/v1`, `Authorization: Bearer <team token>`.

- `GET /token_details`: require `type=teamToken` and the expected numeric team ID.
- `GET /teams/{team_id_or_slug}/projects/{project_slug}`: immutable project/team IDs and original default deployment names.
- `GET /projects/{project_id}/deployment?reference=...`: exact-reference lookup; no prefix search/adoption without a recorded intent.
- `POST /projects/{project_id}/create_deployment`: explicitly send `type=dev`, `isDefault=false`, `reference`, finite Unix-millisecond `expiresAt`; omit `region` unless selected.
- `GET /deployments/{deployment_name}`: reverify exact remote identity.
- `POST /deployments/{deployment_name}/create_deploy_key`: named, deployment-scoped key with matching expiry. The response contains **only `deployKey`**, not its metadata.
- `GET /deployments/{deployment_name}/list_deploy_keys`: array of metadata; `id` is an **integer**, `expiresAt` milliseconds. Verify the uniquely named key here, not by inventing fields on the create response.
- `POST /deployments/{deployment_name}/delete`: exact deployment deletion, never `/projects/.../delete`.

Both deployment and key expiration must be at least 30 minutes in the future; deployment expiry also depends on the team's preview-retention entitlement. The five-day request must fail honestly if unavailable.

Deployment base: verified `https://<deployment>[.<region>].convex.cloud/api/v1`, `Authorization: Convex <deployment key>`.

- `GET /get_canonical_urls`: `{convexCloudUrl, convexSiteUrl}`, both strings. Do not synthesize the HTTP Actions domain from a hostname replacement. M0 rejects custom canonical domains.
- `GET /list_environment_variables`: `{environmentVariables: {KEY: value}}`, **not an array**. Inspect only the public acceptance marker; don't journal the response.
- `POST /update_environment_variables`: `{changes: [{name, value}]}`; M0 writes only `SITE_URL`.

The live runtime check subsequently uses the ordinary public query endpoint `/api/query` (outside `/api/v1`) to verify the fixture's `probe:connection` after the application's CLI pushes it.

## CLI compatibility evidence

Convex npm/CLI **1.45.0** is pinned with integrity hashes in `testdata/native-launch/bun.lock`. The shipped `src/cli/lib/deploymentSelection.ts` was inspected for native loading and deployment-key precedence. It loads `.env.local`/`.env` and prioritizes deployment keys in relevant paths; `CONVEX_OVERRIDE_ACCESS_TOKEN` is another ambient override that a future adapter must audit, in addition to the reserved names in SPEC §11.5.

The binding `CONVEX_DEPLOYMENT=dev:<name>` plus the returned `CONVEX_DEPLOY_KEY=dev:<name>|<token>` was consumed successfully by CLI 1.45.0 through the unchanged root Bun/Turbo launch on Linux/amd64. The live probe verified code push/selection, five-day deployment and key expiry metadata, canonical URLs at the provider-default origin, cross-deployment key rejection, and exact cleanup. Development-default inheritance remains unproven because the public test marker is missing; regional origins and macOS remain untested. The overall live test still fails the defaults subtests. See [docs/M0.md](../../../docs/M0.md) for evidence and the runbook.
