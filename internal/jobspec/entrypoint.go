/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package jobspec

// EntrypointShell is the inline shell snippet locked by IMPL-0001 Q9. It runs
// as the worker container's command, reads the per-shard JSON from the
// mounted ConfigMap (decompressing the .json.gz path when needed), exports
// either RENOVATE_REPOSITORIES (token auth) or RENOVATE_AUTODISCOVER_FILTER
// (GitHub App auth — see INV-0003), and execs the real renovate binary.
//
// Kept short enough to inline cleanly into the Pod spec — no sidecar image,
// no Go helper binary.
const EntrypointShell = `set -eu
INDEX="${JOB_COMPLETION_INDEX:?missing JOB_COMPLETION_INDEX}"
SHARD_FILE="/etc/shards/shard-$(printf '%04d' "$INDEX").json"
GZ_FILE="${SHARD_FILE}.gz"
if   [ -f "$SHARD_FILE" ]; then DATA="$(cat "$SHARD_FILE")"
elif [ -f "$GZ_FILE"    ]; then DATA="$(gunzip -c "$GZ_FILE")"
else echo "shard $INDEX not present (looked at $SHARD_FILE and $GZ_FILE)" >&2; exit 1
fi
REPOS_JSON="$(printf '%s' "$DATA" | jq -c '.repos')"
if [ -n "${RENOVATE_GITHUB_APP_ID:-}" ]; then
  RENOVATE_AUTODISCOVER_FILTER="$REPOS_JSON"; export RENOVATE_AUTODISCOVER_FILTER
else
  RENOVATE_REPOSITORIES="$REPOS_JSON"; export RENOVATE_REPOSITORIES
fi
exec renovate
`

// ShardMountPath is where the shard ConfigMap is mounted in the worker pod.
const ShardMountPath = "/etc/shards"
