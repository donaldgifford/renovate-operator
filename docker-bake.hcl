// docker-bake.hcl — multi-arch build pipeline for renovate-operator.
//
// Targets:
//   - default: local single-arch build (used by `docker buildx bake`)
//   - ci:      multi-arch build for CI verification (no push)
//   - release: multi-arch build + push to GHCR (CI only, gated on tag)
//
// CI workflow consumes this via docker/bake-action@v6 with the `targets`
// input. The release workflow merges in tag-derived image refs from
// docker/metadata-action's bake-file outputs.

variable "REGISTRY" {
  default = "ghcr.io/donaldgifford/renovate-operator"
}

variable "TAG" {
  default = "dev"
}

variable "VERSION" {
  default = "0.0.0-dev"
}

group "default" {
  targets = ["operator"]
}

group "ci" {
  targets = ["operator-ci"]
}

group "release" {
  targets = ["operator-release"]
}

target "_common" {
  context    = "."
  dockerfile = "Dockerfile"
  args = {
    VERSION = "${VERSION}"
  }
  labels = {
    "org.opencontainers.image.source"      = "https://github.com/donaldgifford/renovate-operator"
    "org.opencontainers.image.licenses"    = "Apache-2.0"
    "org.opencontainers.image.description" = "Kubernetes operator running Renovate against multiple Git platforms"
  }
}

// Stub providing default `tags` for local `docker buildx bake`. CI
// runs override this target via docker/metadata-action's
// bake-file-tags output so the bake pushes the same semver-derived
// image refs the metadata-action emits — which is what cosign then
// signs in the next step. operator-release inherits from this and
// does NOT declare tags itself, so the override actually takes
// effect (with HCL inheritance, a child's tags list replaces the
// parent's, not extends it).
target "docker-metadata-action" {
  tags = [
    "${REGISTRY}:${TAG}",
    "${REGISTRY}:latest",
  ]
}

target "operator" {
  inherits = ["_common"]
  tags     = ["${REGISTRY}:${TAG}"]
  platforms = [
    "linux/${BAKE_LOCAL_PLATFORM}",
  ]
}

target "operator-ci" {
  inherits = ["_common"]
  tags     = ["${REGISTRY}:${TAG}-ci"]
  platforms = [
    "linux/amd64",
    "linux/arm64",
  ]
}

target "operator-release" {
  inherits = ["_common", "docker-metadata-action"]
  // tags intentionally omitted — they come from docker-metadata-action
  // (defaults for local bake; CI overrides via metadata-action).
  platforms = [
    "linux/amd64",
    "linux/arm64",
  ]
  output = ["type=registry"]
}
