## [1.3.5](https://github.com/invoraapp/invora-controller/compare/v1.3.4...v1.3.5) (2026-08-22)


### Bug Fixes

* **tax,metric:** adopt existing billing record by code before Create ([#15](https://github.com/invoraapp/invora-controller/issues/15)) ([4bc84b1](https://github.com/invoraapp/invora-controller/commit/4bc84b1e251d697c678a744d63aaede9c949785e)), closes [invora/devops#56](https://github.com/invora/devops/issues/56) [invora/devops#57](https://github.com/invora/devops/issues/57) [invora/devops#109](https://github.com/invora/devops/issues/109)

## [1.3.4](https://github.com/invoraapp/invora-controller/compare/v1.3.3...v1.3.4) (2026-07-20)


### Bug Fixes

* **tap:** resolve the Tap provider by code in the acting org every reconcile (invora-backend[#209](https://github.com/invoraapp/invora-controller/issues/209)) ([#12](https://github.com/invoraapp/invora-controller/issues/12)) ([4933569](https://github.com/invoraapp/invora-controller/commit/4933569467b74df536a479946e8bec1b354e0234))

## [1.3.3](https://github.com/invoraapp/invora-controller/compare/v1.3.2...v1.3.3) (2026-07-18)


### Bug Fixes

* assert acting org via x-zitadel-orgid on org-scoped gateway calls ([10a1ab0](https://github.com/invoraapp/invora-controller/commit/10a1ab02b94994ac9bc3b3728ba670ec7a90e734)), closes [invora/devops#90](https://github.com/invora/devops/issues/90)

## [1.3.2](https://github.com/invoraapp/invora-controller/compare/v1.3.1...v1.3.2) (2026-07-17)


### Bug Fixes

* **org:** adopt existing billing org via GetOrgStatus, not fail on AlreadyExists ([#10](https://github.com/invoraapp/invora-controller/issues/10)) ([21e2d4d](https://github.com/invoraapp/invora-controller/commit/21e2d4def8d3f08b590ef0c7aefd8ac9027c24aa))

## [1.3.1](https://github.com/invoraapp/invora-controller/compare/v1.3.0...v1.3.1) (2026-07-06)


### Bug Fixes

* **plan:** adopt pre-existing plan by code instead of failing on conflict ([#8](https://github.com/invoraapp/invora-controller/issues/8)) ([1cab50d](https://github.com/invoraapp/invora-controller/commit/1cab50ded0594ad6aed2f49730b86cc238a01621))

# [1.3.0](https://github.com/invoraapp/invora-controller/compare/v1.2.2...v1.3.0) (2026-07-05)


### Features

* **subscription:** declarative subscription-level entitlements (connected_business grant) ([#7](https://github.com/invoraapp/invora-controller/issues/7)) ([22bc7f5](https://github.com/invoraapp/invora-controller/commit/22bc7f54b19ebc95fb3a020c7d8d738b07cb87c3))

## [1.2.2](https://github.com/invoraapp/invora-controller/compare/v1.2.1...v1.2.2) (2026-07-01)


### Bug Fixes

* **org:** send Zitadel org GUID as tenant_id, skip non-tenant orgs, backoff ([#6](https://github.com/invoraapp/invora-controller/issues/6)) ([4fafc66](https://github.com/invoraapp/invora-controller/commit/4fafc66c9dd3531f604e458261512660bacabb89))

## [1.2.1](https://github.com/invoraapp/invora-controller/compare/v1.2.0...v1.2.1) (2026-06-22)


### Bug Fixes

* **org:** remove bogus per-org API-key provisioning ([#4](https://github.com/invoraapp/invora-controller/issues/4)) ([8d547ae](https://github.com/invoraapp/invora-controller/commit/8d547aeecf4ff488889c469f8014a477f6fde9ba))

# [1.2.0](https://github.com/invoraapp/invora-controller/compare/v1.1.0...v1.2.0) (2026-06-22)


### Features

* **controller:** gRPC AdminClient + refreshed BSR stubs ([#3](https://github.com/invoraapp/invora-controller/issues/3)) ([d1c68fc](https://github.com/invoraapp/invora-controller/commit/d1c68fc6b3a5d29489aac2d8d3c6749ab7caaad1))

# [1.1.0](https://github.com/invoraapp/invora-controller/compare/v1.0.1...v1.1.0) (2026-06-17)


### Features

* **billing:** migrate org admin to BillingOrgAdminService gRPC ([#1](https://github.com/invoraapp/invora-controller/issues/1)) ([8920f39](https://github.com/invoraapp/invora-controller/commit/8920f39bba1f250f8050f70b4380b70b0cfb934b))

## [1.0.1](https://github.com/invoraapp/invora-controller/compare/v1.0.0...v1.0.1) (2026-06-17)


### Bug Fixes

* **gateway:** dial gateway by host:port, not the raw URL ([#2](https://github.com/invoraapp/invora-controller/issues/2)) ([e7b5a01](https://github.com/invoraapp/invora-controller/commit/e7b5a0189bc23ef59424b8b21124b39be8897ae4))

# 1.0.0 (2026-05-25)


### Bug Fixes

* address all critic review findings ([3adfc8c](https://github.com/invoraapp/invora-controller/commit/3adfc8cb83b7620edcf0af77e1c24a2b1a55f41c))
* **ci:** build golangci-lint from source for Go 1.25 compat ([8522c60](https://github.com/invoraapp/invora-controller/commit/8522c60fd050f95cfea9b4b2ec5fd8b2400837c9))
* **docker:** build for TARGETARCH in multi-platform release ([10121f8](https://github.com/invoraapp/invora-controller/commit/10121f8f1a38c5c0c0237125f04278b81452b059))


### Features

* add convert package + prep for billing gRPC migration ([94c26bc](https://github.com/invoraapp/invora-controller/commit/94c26bc8f8a9e622b214abfb90682012108b4f7e))
* add core + invoicing API groups, remove BillingEntity ([915defa](https://github.com/invoraapp/invora-controller/commit/915defae71b67da8314aa8767d95e7e8f7aff051))
* add gRPC connection support to orgResourceContext ([64d9362](https://github.com/invoraapp/invora-controller/commit/64d9362cdba276bd2c100df3460520af8128fa88))
* add payment provider CRDs (Stripe, Adyen, GoCardless, Generic) + Wallet ([b04b8f2](https://github.com/invoraapp/invora-controller/commit/b04b8f2f2451ee9f4157e1fe34356ce710b42e5b))
* **billing:** add AdminClient for org admin gRPC-JSON ops ([48a5b07](https://github.com/invoraapp/invora-controller/commit/48a5b07e8f17bc4df5d79793ea09f07118587309))
* **core:** add unified InvoraInstance CRD ([5013f42](https://github.com/invoraapp/invora-controller/commit/5013f4291b5006228c945695953c7e1c9e4b8f32))
* enable automated releases ([aca9f95](https://github.com/invoraapp/invora-controller/commit/aca9f95e76276a000a6edf5223645e794f7b54ee))
* implement controllers for core + invoicing CRDs ([23616c3](https://github.com/invoraapp/invora-controller/commit/23616c3e4204a64fc2cab6101ea6aff256956015))
* implement real gRPC controllers + buf-generated proto stubs ([0264a74](https://github.com/invoraapp/invora-controller/commit/0264a74d996e462baa1cfdd25076691ab2f1dc9b))
* initial release of Invora Billing Controller ([9d2ced0](https://github.com/invoraapp/invora-controller/commit/9d2ced0140cca6f11b649d587fed24e1693d683a))
* migrate billing CRD controllers to gRPC ([6df82df](https://github.com/invoraapp/invora-controller/commit/6df82df6f93f32df88c7bcedbf44216297e5d397))
* migrate Plan controller to gRPC + update Helm template names ([0fdba10](https://github.com/invoraapp/invora-controller/commit/0fdba101e534ab209680da6af015445f9317fa72))
