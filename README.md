# Invora Controller

[![CI](https://github.com/invoraapp/invora-controller/actions/workflows/ci.yaml/badge.svg)](https://github.com/invoraapp/invora-controller/actions/workflows/ci.yaml)

Kubernetes operator that manages [Invora](https://invora.app) billing resources declaratively via Custom Resource Definitions (CRDs). Define your billing configuration as YAML — plans, customers, subscriptions, payment providers — and let the controller reconcile it against the Invora platform.

## Features

- **GitOps-native billing** — manage billing config alongside your infrastructure
- **19 CRD types** covering plans, customers, subscriptions, taxes, payment providers, and more
- **Per-provider payment CRDs** — type-safe schemas for Tap, Stripe, Adyen, GoCardless
- **Generic payment provider** — extensible CRD for future/custom integrations
- **Multi-tenant** — single controller instance manages multiple organizations
- **Environment-agnostic** — works on dev, staging, production, and self-hosted
- **Zitadel integration** — auto-provisions billing orgs from identity events
- **Helm chart included** — deploy with a single `helm install`

## Quick Start

### Install CRDs

```bash
kubectl apply -f https://raw.githubusercontent.com/invoraapp/invora-controller/main/config/crd/bases/
```

### Install via Helm

```bash
helm install invora-controller oci://ghcr.io/invoraapp/invora-controller/charts/invora-billing-controller \
  --set image.tag=latest \
  --set watchNamespace=my-billing-namespace
```

### Define Your Billing

```yaml
apiVersion: billing.invora.app/v1alpha1
kind: InvoraBillingInstance
metadata:
  name: my-billing
  namespace: billing
spec:
  gatewayUrl: "https://gateway.invora.app"
  tokenRef:
    name: invora-sa-token
    key: token
---
apiVersion: billing.invora.app/v1alpha1
kind: InvoraBillingOrganization
metadata:
  name: my-company
  namespace: billing
spec:
  instanceRef:
    name: my-billing
  name: "My Company"
  email: "billing@mycompany.com"
  currency: "USD"
  timezone: "America/New_York"
  writeSecretToRef:
    name: my-company-billing-credentials
---
apiVersion: billing.invora.app/v1alpha1
kind: InvoraBillingPlan
metadata:
  name: starter
  namespace: billing
spec:
  organizationRef:
    name: my-company
  code: "starter"
  name: "Starter Plan"
  amountCents: 2900
  amountCurrency: "USD"
  interval: "monthly"
  payInAdvance: true
---
apiVersion: billing.invora.app/v1alpha1
kind: InvoraBillingStripeProvider
metadata:
  name: stripe-prod
  namespace: billing
spec:
  organizationRef:
    name: my-company
  code: "stripe_prod"
  name: "Stripe Production"
  secretKeyRef:
    name: stripe-credentials
    key: secretKey
  webhookSecretRef:
    name: stripe-credentials
    key: webhookSecret
  successRedirectUrl: "https://mycompany.com/billing/success"
```

## CRD Types

| Kind | Short | Description |
|------|-------|-------------|
| `InvoraBillingInstance` | — | Connection to an Invora gateway |
| `InvoraBillingOrganization` | — | Billing tenant/organization |
| `InvoraBillingPlan` | — | Subscription plan definition |
| `InvoraBillingCustomer` | — | Billing customer |
| `InvoraBillingSubscription` | — | Customer subscription |
| `InvoraBillingTax` | — | Tax rate |
| `InvoraBillingAddon` | — | One-time charge add-on |
| `InvoraBillingCoupon` | — | Discount coupon |
| `InvoraBillingFeature` | — | Plan entitlement feature |
| `InvoraBillingMetric` | — | Usage-based billable metric |
| `InvoraBillingWebhookEndpoint` | — | Webhook delivery endpoint |
| `InvoraBillingEntity` | — | Legal billing entity |
| `InvoraBillingWallet` | `iwallet` | Prepaid credit wallet |
| `InvoraBillingTapProvider` | `ltap` | Tap Payments provider |
| `InvoraBillingStripeProvider` | `istripe` | Stripe provider |
| `InvoraBillingAdyenProvider` | `iadyen` | Adyen provider |
| `InvoraBillingGoCardlessProvider` | `igc` | GoCardless provider |
| `InvoraBillingPaymentProvider` | `ipay` | Generic payment provider |

## Architecture

```
┌─────────────────────────────────────────────────────┐
│  Kubernetes Cluster                                  │
│                                                      │
│  ┌──────────────┐     ┌─────────────────────────┐  │
│  │ invora-      │────>│ Invora Gateway          │  │
│  │ controller   │     │ (gRPC-JSON transcoding) │  │
│  └──────┬───────┘     └────────────┬────────────┘  │
│         │                          │                 │
│  ┌──────┴───────┐     ┌───────────┴─────────────┐  │
│  │ CRDs         │     │ Invora Billing Backend   │  │
│  │ (desired     │     │ (manages subscriptions,  │  │
│  │  state)      │     │  invoices, payments)     │  │
│  └──────────────┘     └─────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

The controller watches CRDs and reconciles them against the Invora billing backend through the gateway's gRPC-JSON transcoding endpoint. Authentication uses service account Bearer tokens with per-org scoping via `x-invora-org-id` headers.

## Configuration

### Helm Values

```yaml
image:
  repository: ghcr.io/invoraapp/invora-controller
  tag: latest

watchNamespace: ""  # empty = cluster-wide

zitadelSubscriber:
  enabled: false
  zitadelDomain: "auth.example.com"
  billingInstance:
    name: "my-billing"
    namespace: "billing"
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `WATCH_NAMESPACE` | Comma-separated namespaces (empty = all) |
| `BILLING_INSTANCE_NAME` | Zitadel subscriber target instance |
| `BILLING_INSTANCE_NAMESPACE` | Zitadel subscriber target namespace |
| `ZITADEL_DOMAIN` | Zitadel domain for event subscriber |
| `INVORA_ENV` | Environment (tenant ns = `invora-{env}`) |

## Development

```bash
# Prerequisites: Go 1.25+, controller-gen, Docker

# Build
go build ./...

# Test
go test ./... -v -race

# Generate deepcopy + CRD manifests
make generate manifests

# Run locally against current kubeconfig
go run ./cmd/main.go

# Build Docker image
docker build -t invora-controller:dev .
```

## License

Apache License 2.0 — see [LICENSE](LICENSE) for details.
