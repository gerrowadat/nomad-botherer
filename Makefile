BINARY     := nomad-botherer
CTL_BINARY := nbctl
MODULE     := github.com/gerrowadat/nomad-botherer

IMAGE      ?= ghcr.io/gerrowadat/$(BINARY)
PLATFORMS  := linux/amd64,linux/arm64

# Version variables — used by 'install' targets (go install), Docker build args,
# and the 'version' target. Bazel release builds use tools/workspace_status.sh.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDDATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS    := -X main.version=$(VERSION) \
              -X main.commit=$(COMMIT) \
              -X main.buildDate=$(BUILDDATE) \
              -s -w

CTL_LDFLAGS := -X main.version=$(VERSION) -s -w

.PHONY: all build build-server build-ctl install install-server install-ctl \
        test test-cover lint gazelle generate clean \
        docker docker-push \
        release-patch release-minor release-major version

all: build

## build: build both binaries with Bazel
build:
	bazel build //cmd/nomad-botherer //cmd/nbctl

## build-server: build the nomad-botherer server
build-server:
	bazel build //cmd/nomad-botherer

## build-ctl: build the nbctl CLI
build-ctl:
	bazel build //cmd/nbctl

## install: install both binaries to $GOPATH/bin using go install
install: install-server install-ctl

## install-server: go install the server binary
install-server:
	go install -ldflags "$(LDFLAGS)" ./cmd/nomad-botherer

## install-ctl: go install the nbctl binary
install-ctl:
	go install -ldflags "$(CTL_LDFLAGS)" ./cmd/nbctl

## test: run all tests with Bazel (includes race detector)
test:
	bazel test //...

## test-cover: run tests and generate a coverage report (uses go test for coverage output format)
test-cover:
	go test -race -timeout 60s -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## lint: run go vet
lint:
	go vet ./...

## gazelle: regenerate BUILD.bazel files and sync MODULE.bazel use_repo list
gazelle:
	bazel run //:gazelle
	bazel mod tidy

# Pinned tool versions — must match the versions recorded in the generated file headers.
BUF_VERSION                := v1.68.4
PROTOC_GEN_GO_VERSION      := v1.36.11
PROTOC_GEN_GO_GRPC_VERSION := v1.6.1

## generate: regenerate protobuf code from proto/nomad_botherer.proto
generate:
	go install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	go install google.golang.org/protobuf/cmd/protoc-gen-go@$(PROTOC_GEN_GO_VERSION)
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@$(PROTOC_GEN_GO_GRPC_VERSION)
	buf generate

## clean: remove Bazel and go test build artefacts
clean:
	bazel clean
	rm -f coverage.out coverage.html
	rm -f nomad-botherer nbctl

## version: print the current version
version:
	@echo $(VERSION)

# ── Docker ──────────────────────────────────────────────────────────────────────
# Docker builds use go build directly inside the Dockerfile for simplicity.
# Bazel is used for local development and CI.

## docker: build a multi-platform image (requires docker buildx)
docker:
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILDDATE=$(BUILDDATE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		.

## docker-push: build and push a multi-platform image
docker-push:
	docker buildx build \
		--platform $(PLATFORMS) \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILDDATE=$(BUILDDATE) \
		-t $(IMAGE):$(VERSION) \
		-t $(IMAGE):latest \
		--push \
		.

# ── Releases (semver git tags) ──────────────────────────────────────────────────
# Usage: make release-patch   (1.2.3 → 1.2.4)
#        make release-minor   (1.2.3 → 1.3.0)
#        make release-major   (1.2.3 → 2.0.0)

_CURRENT_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0")
_MAJOR       := $(shell echo $(_CURRENT_TAG) | sed 's/v//' | cut -d. -f1)
_MINOR       := $(shell echo $(_CURRENT_TAG) | sed 's/v//' | cut -d. -f2)
_PATCH       := $(shell echo $(_CURRENT_TAG) | sed 's/v//' | cut -d. -f3)

release-patch:
	@NEW_TAG=v$(_MAJOR).$(_MINOR).$(shell echo $$(( $(_PATCH) + 1 ))); \
	echo "Tagging $$NEW_TAG"; \
	git tag -a $$NEW_TAG -m "Release $$NEW_TAG"; \
	echo "Push with: git push origin $$NEW_TAG"

release-minor:
	@NEW_TAG=v$(_MAJOR).$(shell echo $$(( $(_MINOR) + 1 ))).0; \
	echo "Tagging $$NEW_TAG"; \
	git tag -a $$NEW_TAG -m "Release $$NEW_TAG"; \
	echo "Push with: git push origin $$NEW_TAG"

release-major:
	@NEW_TAG=v$(shell echo $$(( $(_MAJOR) + 1 ))).0.0; \
	echo "Tagging $$NEW_TAG"; \
	git tag -a $$NEW_TAG -m "Release $$NEW_TAG"; \
	echo "Push with: git push origin $$NEW_TAG"
