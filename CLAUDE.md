# Invora Controller — Claude Code Instructions

Kubernetes operator for managing Invora Billing resources via CRDs. Go + Kubebuilder. Talks to Invora gateway via gRPC-JSON transcoding.

## Critical Rules

1. **NEVER expose internal billing backend details** in public APIs, CRDs, or logs.
2. **`controller-gen object paths="./api/v1alpha1"` + `controller-gen crd paths="./api/v1alpha1" output:crd:dir=config/crd/bases`** after every type change.
3. **Copy CRDs to Helm chart** after regenerating: `cp config/crd/bases/*.yaml charts/invora-billing-controller/crds/`
4. **Per-provider CRDs** — never a generic config block. Each payment provider gets its own typed CRD.
5. **Environment-agnostic** — the controller works on dev/stg/prod/self-hosted. No hardcoded URLs.

## Architecture

```
InvoraBillingInstance (gatewayUrl + tokenRef)
  └── InvoraBillingOrganization (tenant)
        ├── Plans, Customers, Subscriptions
        ├── Taxes, Addons, Coupons, Features, Metrics
        ├── WebhookEndpoints, BillingEntities
        ├── Wallets (prepaid credits)
        └── Payment Providers (Tap, Stripe, Adyen, GoCardless, Generic)
```

## Commands

```bash
go build ./...                          # Build
go test ./... -v -race                  # Test
make generate                           # Deepcopy methods
make manifests                          # CRD YAML generation

# Run locally
go run ./cmd/main.go --metrics-bind-address=:8080 --health-probe-bind-address=:8081

# Docker
docker build -t ghcr.io/invoraapp/invora-controller:latest .

# Helm
helm install invora charts/invora-billing-controller/ \
  --set image.tag=latest \
  --set watchNamespace=my-namespace
```

## Adding a New CRD

1. Create `api/v1alpha1/<name>_types.go` with Spec + Status + List types
2. Add `func init() { SchemeBuilder.Register(&Type{}, &TypeList{}) }`
3. Run `make generate manifests`
4. Copy CRDs: `cp config/crd/bases/*.yaml charts/invora-billing-controller/crds/`
5. Create controller at `internal/controller/<name>_controller.go`
6. Register in `cmd/main.go`
7. Add client methods in `internal/billingclient/client.go`

## Key Paths

| Component | Path |
|-----------|------|
| CRD types | `api/v1alpha1/` |
| Controllers | `internal/controller/` |
| Billing client | `internal/billingclient/` |
| Helm chart | `charts/invora-billing-controller/` |
| CRD manifests | `config/crd/bases/` |
| Entry point | `cmd/main.go` |
| CI | `.github/workflows/ci.yaml` |
| Release | `.github/workflows/release.yaml` |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `WATCH_NAMESPACE` | Comma-separated namespaces to watch (empty = cluster-wide) |
| `BILLING_INSTANCE_NAME` | Zitadel subscriber: target InvoraBillingInstance |
| `BILLING_INSTANCE_NAMESPACE` | Zitadel subscriber: target namespace |
| `ZITADEL_DOMAIN` | Zitadel domain for event subscriber |
| `INVORA_ENV` | Environment name (tenant namespace = `invora-{env}`) |
