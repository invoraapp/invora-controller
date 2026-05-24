# Invora Billing Controller

Kubernetes operator for managing Invora Billing resources via CRDs. Communicates with the Invora Billing gateway (gRPC-JSON transcoding) to reconcile billing entities.

## API Group

`billing.invora.app/v1alpha1`

## CRD Types

| Kind | Description |
|------|-------------|
| InvoraBillingInstance | Connection to an Invora Billing gateway |
| InvoraBillingOrganization | Billing organization (tenant) |
| InvoraBillingPlan | Subscription plan |
| InvoraBillingCustomer | Billing customer |
| InvoraBillingSubscription | Customer subscription to a plan |
| InvoraBillingTax | Tax rate definition |
| InvoraBillingAddon | One-time charge add-on |
| InvoraBillingCoupon | Discount coupon |
| InvoraBillingFeature | Feature flag for plan entitlements |
| InvoraBillingMetric | Billable metric for usage-based pricing |
| InvoraBillingWebhookEndpoint | Webhook delivery endpoint |
| InvoraBillingTapProvider | Tap Payments payment provider |
| InvoraBillingEntity | Legal billing entity |

## Commands

```bash
# Build
go build ./...

# Generate CRD manifests (requires controller-gen)
make manifests

# Generate deepcopy
make generate

# Run locally
go run ./cmd/main.go --metrics-bind-address=:8080 --health-probe-bind-address=:8081

# Docker
docker build -t invora-billing-controller:latest .

# Helm
helm install invora-billing charts/invora-billing-controller/ \
  --set image.tag=latest \
  --set watchNamespace=bayader-billing
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `WATCH_NAMESPACE` | Comma-separated namespaces to watch (empty = all) |
| `BILLING_INSTANCE_NAME` | For Zitadel subscriber: target InvoraBillingInstance name |
| `BILLING_INSTANCE_NAMESPACE` | For Zitadel subscriber: target InvoraBillingInstance namespace |
| `ZITADEL_DOMAIN` | Zitadel domain for event subscriber |
| `INVORA_ENV` | Environment name (used for tenant namespace) |

## Key Paths

| Component | Path |
|-----------|------|
| CRD types | `api/v1alpha1/` |
| Controllers | `internal/controller/` |
| Billing client | `internal/billingclient/` |
| Helm chart | `charts/invora-billing-controller/` |
| CRD manifests | `config/crd/bases/` |
| Entry point | `cmd/main.go` |
