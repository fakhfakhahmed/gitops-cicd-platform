# =============================================================================
# gitops-cicd-platform - developer entrypoint
# =============================================================================
# Every target here is also what CI runs, so "it works on my machine" and
# "it works in the pipeline" converge. If you change a build flag, change it
# here and the workflow will inherit it.
#
# On Windows, run these through Git Bash / WSL, or use the equivalent
# PowerShell scripts in ./scripts which are the first-class path on this host.
# =============================================================================

# Fail fast and loudly: any failing command in a recipe aborts the target.
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

# -----------------------------------------------------------------------------
# Variables
# -----------------------------------------------------------------------------
APP_DIR       ?= app
CHART_DIR     ?= charts/app
REGISTRY      ?= ghcr.io
IMAGE_NAME    ?= fakhfakhahmed/gitops-cicd-platform/app
IMAGE         ?= $(REGISTRY)/$(IMAGE_NAME)

# VERSION comes from the nearest git tag, falling back to 0.1.0-dev so the
# build still works in a fresh clone with no tags.
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0-dev")
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE    ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

# Inject build metadata into the binary. These are the same ldflags the
# Dockerfile uses - the /  endpoint echoes them back, which is how we prove
# end-to-end that the artifact running in the cluster is the one CI built.
LDFLAGS       := -s -w \
                 -X main.version=$(VERSION) \
                 -X main.commit=$(COMMIT) \
                 -X main.buildDate=$(BUILD_DATE)

KUBE_NAMESPACE ?= app-dev
ARGOCD_NS      ?= argocd
ARGOCD_VERSION ?= v2.13.2
MINIKUBE_PROFILE ?= gitops-platform

# Colours for human-readable output (disabled when not a TTY).
CYAN  := \033[36m
BOLD  := \033[1m
RESET := \033[0m

.DEFAULT_GOAL := help

.PHONY: help build test lint docker-build chart-lint chart-template \
        cluster-up cluster-down argocd-install argocd-password bootstrap

# -----------------------------------------------------------------------------
# help - self-documenting. Any target with a `## comment` is listed.
# -----------------------------------------------------------------------------
help: ## Show this help message
	@printf "\n$(BOLD)gitops-cicd-platform$(RESET)\n"
	@printf "Production-grade GitHub Actions -> GHCR -> Argo CD delivery pipeline.\n\n"
	@printf "$(BOLD)Usage:$(RESET)\n  make $(CYAN)<target>$(RESET)\n\n"
	@printf "$(BOLD)Targets:$(RESET)\n"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  $(CYAN)%-18s$(RESET) %s\n", $$1, $$2}'
	@printf "\n$(BOLD)Current build identity:$(RESET)\n"
	@printf "  image   %s\n" "$(IMAGE)"
	@printf "  version %s\n" "$(VERSION)"
	@printf "  commit  %s\n\n" "$(COMMIT)"

# -----------------------------------------------------------------------------
# Application
# -----------------------------------------------------------------------------
build: ## Compile the Go service to ./bin/app (static, stripped)
	@printf "$(CYAN)==>$(RESET) building $(IMAGE):$(VERSION)\n"
	cd $(APP_DIR) && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../bin/app .
	@printf "$(CYAN)==>$(RESET) wrote ./bin/app\n"

test: ## Run unit tests with the race detector and write a coverage profile
	@printf "$(CYAN)==>$(RESET) running tests\n"
	cd $(APP_DIR) && go test -race -covermode=atomic -coverprofile=../coverage.out ./...
	@cd $(APP_DIR) && go tool cover -func=../coverage.out | tail -n 1

lint: ## Run golangci-lint over the Go module
	@printf "$(CYAN)==>$(RESET) linting Go\n"
	cd $(APP_DIR) && golangci-lint run --config ../.golangci.yml ./...

docker-build: ## Build the container image locally (single-arch, no push)
	@printf "$(CYAN)==>$(RESET) docker build $(IMAGE):$(VERSION)\n"
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		-f $(APP_DIR)/Dockerfile $(APP_DIR)

# -----------------------------------------------------------------------------
# Helm chart
# -----------------------------------------------------------------------------
chart-lint: ## Lint the Helm chart against every environment's values file
	@printf "$(CYAN)==>$(RESET) helm lint (base values)\n"
	helm lint $(CHART_DIR)
	@for env in dev staging prod; do \
		printf "$(CYAN)==>$(RESET) helm lint (%s overlay)\n" "$$env"; \
		helm lint $(CHART_DIR) --values gitops/environments/$$env/values.yaml; \
	done

chart-template: ## Render the chart for every environment into ./rendered
	@mkdir -p rendered
	@for env in dev staging prod; do \
		printf "$(CYAN)==>$(RESET) rendering %s\n" "$$env"; \
		helm template app $(CHART_DIR) \
			--namespace app-$$env \
			--values gitops/environments/$$env/values.yaml \
			> rendered/$$env.yaml; \
	done
	@printf "$(CYAN)==>$(RESET) rendered manifests in ./rendered\n"
	@printf "    validate with: kubeconform -strict -summary rendered/*.yaml\n"

# -----------------------------------------------------------------------------
# Local cluster + Argo CD
# -----------------------------------------------------------------------------
cluster-up: ## Start the local minikube cluster with ingress + metrics-server
	@printf "$(CYAN)==>$(RESET) starting minikube profile '$(MINIKUBE_PROFILE)'\n"
	minikube start --profile $(MINIKUBE_PROFILE) --cpus=4 --memory=6144 --driver=docker
	minikube addons enable ingress --profile $(MINIKUBE_PROFILE)
	minikube addons enable metrics-server --profile $(MINIKUBE_PROFILE)
	@kubectl cluster-info --context $(MINIKUBE_PROFILE)

cluster-down: ## Delete the local minikube cluster
	@printf "$(CYAN)==>$(RESET) deleting minikube profile '$(MINIKUBE_PROFILE)'\n"
	minikube delete --profile $(MINIKUBE_PROFILE)

argocd-install: ## Install Argo CD into the cluster and wait for it to be ready
	@printf "$(CYAN)==>$(RESET) installing Argo CD $(ARGOCD_VERSION)\n"
	kubectl apply -f gitops/bootstrap/namespace.yaml
	kubectl apply -n $(ARGOCD_NS) \
		-f https://raw.githubusercontent.com/argoproj/argo-cd/$(ARGOCD_VERSION)/manifests/install.yaml
	kubectl rollout status deployment/argocd-server -n $(ARGOCD_NS) --timeout=300s
	kubectl apply -f gitops/applications/appproject.yaml
	kubectl apply -f gitops/bootstrap/root-app.yaml

argocd-password: ## Print the generated Argo CD admin password
	@kubectl -n $(ARGOCD_NS) get secret argocd-initial-admin-secret \
		-o jsonpath='{.data.password}' | base64 -d; echo

bootstrap: cluster-up argocd-install ## One-shot: cluster + Argo CD + root app
	@printf "\n$(BOLD)Bootstrap complete.$(RESET)\n"
	@printf "  1. kubectl port-forward svc/argocd-server -n $(ARGOCD_NS) 8080:443\n"
	@printf "  2. open https://localhost:8080  (user: admin)\n"
	@printf "  3. make argocd-password\n\n"
