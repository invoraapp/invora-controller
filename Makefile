IMG ?= europe-west1-docker.pkg.dev/inpro-invora/invora-tools/invora-billing-controller:latest

CONTROLLER_GEN_VERSION ?= v0.17.2

LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

.PHONY: all
all: generate fmt vet build

##@ Development

.PHONY: generate
generate: controller-gen ## Generate deepcopy methods
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen ## Generate CRD manifests
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: fmt
fmt: ## Run gofmt
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: test
test: generate fmt vet ## Run tests
	go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: generate fmt vet ## Build manager binary
	go build -o bin/manager ./cmd/main.go

.PHONY: docker-build
docker-build: ## Build Docker image
	docker build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push Docker image
	docker push $(IMG)

##@ Deployment

.PHONY: install
install: manifests ## Install CRDs into the cluster
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the cluster
	kubectl delete -f config/crd/bases

.PHONY: deploy
deploy: manifests ## Deploy controller to the cluster
	kubectl apply -k config/default

.PHONY: undeploy
undeploy: ## Undeploy controller from the cluster
	kubectl delete -k config/default

##@ Helm

.PHONY: helm-crds
helm-crds: manifests ## Copy CRD manifests into Helm chart crds/ directory
	cp config/crd/bases/*.yaml charts/invora-billing-controller/crds/

##@ Tools

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen
$(CONTROLLER_GEN): $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

$(LOCALBIN):
	mkdir -p $(LOCALBIN)
