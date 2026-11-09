#!/usr/bin/env bash
# Build and push the image to the lab registry. TAG defaults to the short git sha.
set -euo pipefail
REGISTRY=${REGISTRY:-192.168.0.140:5555/mcp-bifrost}
TAG=${TAG:-$(git rev-parse --short HEAD)}
docker build --build-arg VERSION="$TAG" -t "$REGISTRY/gateway:$TAG" -t "$REGISTRY/gateway:latest" .
docker push -q "$REGISTRY/gateway:$TAG"
docker push -q "$REGISTRY/gateway:latest"
echo "pushed $REGISTRY/gateway:$TAG"
