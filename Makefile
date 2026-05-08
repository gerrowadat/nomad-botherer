BINARY     := nomad-botherer
CTL_BINARY := nbctl
MODULE     := github.com/gerrowadat/nomad-botherer
CMD        := ./cmd/nomad-botherer
CTL_CMD    := ./cmd/nbctl

# Version is derived from the most recent git tag; falls back to "dev".
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT     := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILDDATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS    := -X main.version=$(VERSION) \
              -X main.commit=$(COMMIT) \
              -X main.buildDate=$(BUILDDATE) \
              -s -w

CTL_LDFLAGS := -X main.version=$(VERSION) -s -w

IMAGE      ?= ghcr.io/gerrowadat/$(BINARY)
PLATFORMS  := linux/amd64,linux/arm64

.PHONY: all build build-server build-ctl install install-server install-ctl test lint clean docker docker-push release-patch release-minor release-major version

all: build

## build: compile both binaries for the current platform
build: build-server build-ctl

## build-server: compile the nomad-botherer server
build-server:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

## build-ctl: compile the nbctl CLI
build-ctl:
	go build -ldflags "$(CTL_LDFLAGS)" -o $(CTL_BINARY) $(CTL_CMD)

## install: install both binaries to $GOPATH/bin (or go install equivalent)
install: install-server install-ctl

## install-server: go install the server binary
install-server:
	go install -ldflags "$(LDFLAGS)" $(CMD)

## install-ctl: go install the nbctl binary
install-ctl:
	go install -ldflags "$(CTL_LDFLAGS)" $(CTL_CMD)

## test: run all tests
test:
	go test -race -timeout 60s ./...

## test-cover: run tests with coverage report
test-cover:
	go test -race -timeout 60s -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## lint: run go vet
lint:
	go vet ./...

## clean: remove build artefacts
clean:
	rm -f $(BINARY) $(CTL_BINARY) coverage.out coverage.html

## version: print the current version
version:
	@echo $(VERSION)

# ── Docker ──────────────────────────────────────────────────────────────────────

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
