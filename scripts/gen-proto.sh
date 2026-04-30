#!/usr/bin/env bash
# Regenerate Go code from proto/. Requires buf and protoc-gen-go on PATH.
set -euo pipefail
cd "$(dirname "$0")/../proto"
buf generate
