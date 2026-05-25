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
