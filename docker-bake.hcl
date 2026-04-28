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
  inherits = ["_common"]
  tags = [
    "${REGISTRY}:${TAG}",
    "${REGISTRY}:latest",
  ]
  platforms = [
    "linux/amd64",
    "linux/arm64",
  ]
  output = ["type=registry"]
}
