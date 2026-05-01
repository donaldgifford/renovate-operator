# renovate-operator — task runner
#
# Wraps the kubebuilder-generated Makefile for ergonomics and adds
# project-specific recipes (license, release, composite gates) carried
# over from Makefile.old. The kubebuilder Makefile is left intact —
# every `make <target>` continues to work directly.

set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

allowed_licenses := "Apache-2.0,MIT,BSD-2-Clause,BSD-3-Clause,ISC,MPL-2.0"

# Default: list recipes
_default:
    @just --list --unsorted

# ─── Generate & dev ─────────────────────────────────────────────────

# Regenerate CRDs, RBAC, and webhooks from kubebuilder markers
[group('dev')]
manifests:
    @make manifests

# Regenerate DeepCopy method implementations
[group('dev')]
generate:
    @make generate

# go fmt
[group('dev')]
fmt:
    @make fmt

# go vet
[group('dev')]
vet:
    @make vet

# Run the controller from your host
[group('dev')]
run:
    @make run

# ─── Build ──────────────────────────────────────────────────────────

# Build the manager binary into bin/manager
[group('build')]
build:
    @make build

# Build the docker image
[group('build')]
docker-build:
    @make docker-build

# Push the docker image
[group('build')]
docker-push:
    @make docker-push

# Multi-arch image build via buildx
[group('build')]
docker-buildx:
    @make docker-buildx

# Generate consolidated YAML installer at dist/install.yaml
[group('build')]
build-installer:
    @make build-installer

# ─── Test ───────────────────────────────────────────────────────────

# Run unit tests (manifests + generate + fmt + vet + test)
[group('test')]
test:
    @make test

# Run tests for a single package: just test-pkg ./internal/controller
[group('test')]
test-pkg pkg:
    go test -v -race {{ pkg }}

# Run tests with coverage profile written to coverage.out
[group('test')]
test-coverage:
    go test -v -race -coverprofile=coverage.out ./...

# Open the HTML coverage report
[group('test')]
test-report:
    go test -coverprofile=coverage.out ./...
    go tool cover -html=coverage.out

# Run e2e tests against a Kind cluster
[group('test')]
test-e2e:
    @make test-e2e

# Set up the e2e Kind cluster (without running tests)
[group('test')]
setup-test-e2e:
    @make setup-test-e2e

# Tear down the e2e Kind cluster
[group('test')]
cleanup-test-e2e:
    @make cleanup-test-e2e

# ─── Lint ───────────────────────────────────────────────────────────

# Run golangci-lint
[group('lint')]
lint:
    @make lint

# Run golangci-lint with --fix
[group('lint')]
lint-fix:
    @make lint-fix

# Verify the golangci-lint configuration
[group('lint')]
lint-config:
    @make lint-config

# ─── License compliance ─────────────────────────────────────────────

# Check dependency licenses against the allow list
[group('license')]
license-check:
    go-licenses check ./... --allowed_licenses={{ allowed_licenses }}

# Generate CSV report of all dependency licenses
[group('license')]
license-report:
    go-licenses report ./... --template=.github/licenses-csv.tpl

# ─── Cluster ────────────────────────────────────────────────────────

# Install CRDs into the current K8s cluster
[group('cluster')]
install:
    @make install

# Uninstall CRDs from the current K8s cluster
[group('cluster')]
uninstall:
    @make uninstall

# Deploy the controller to the current K8s cluster
[group('cluster')]
deploy:
    @make deploy

# Undeploy the controller from the current K8s cluster
[group('cluster')]
undeploy:
    @make undeploy

# ─── Release ────────────────────────────────────────────────────────

# Validate the goreleaser config
[group('release')]
release-check:
    goreleaser check

# Snapshot release locally (no publish, no sign)
[group('release')]
release-local:
    goreleaser release --snapshot --clean --skip=publish --skip=sign

# Tag and push a new release: just release v0.1.0
[group('release')]
release tag:
    git tag -a {{ tag }} -m "Release {{ tag }}"
    git push origin {{ tag }}

# ─── Composite gates ────────────────────────────────────────────────

# Pre-commit gate: lint + test
[group('gate')]
check: lint test
    @echo "✓ Pre-commit checks passed"

# Full CI gate: lint + test + build + license-check
[group('gate')]
ci: lint test build license-check
    @echo "✓ CI pipeline complete"
