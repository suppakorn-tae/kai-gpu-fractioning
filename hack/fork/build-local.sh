#!/usr/bin/env bash
# Build the four images with upstream's Makefile targets (needs BuildKit, i.e.
# the docker buildx plugin) and import them into RKE2's containerd, for a
# single-node cluster without a registry.
#
# Usage: hack/fork/build-local.sh [TAG]     (default v0.1.6-sm.1)
set -euo pipefail
cd "$(dirname "$0")/../.."
TAG=${1:-v0.1.6-sm.1}
REPO=localhost/gpu-fractioning

docker buildx version >/dev/null || { echo "docker buildx plugin required" >&2; exit 1; }
make docker-build DOCKER_REPO_BASE="$REPO" VERSION="$TAG" PLATFORM=linux/amd64

if [[ "${IMPORT:-1}" == 1 ]]; then
  for img in operator mpsd fractiond metricsd; do
    docker save "$REPO/$img:$TAG" | sudo /var/lib/rancher/rke2/bin/ctr \
      --address /run/k3s/containerd/containerd.sock --namespace k8s.io images import - >/dev/null
    echo "imported $REPO/$img:$TAG"
  done
fi
