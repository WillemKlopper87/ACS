#!/usr/bin/env bash
# Regenerate the USP protobuf bindings.
#
# The vendored .proto files are upstream bytes and carry no
# `option go_package`, so the Go import path is supplied here with -M
# flags rather than by editing files we do not own. Both proto packages
# (usp_record and usp) map to the single Go package uspproto: they are
# always used together and share no top-level type names.
#
# Requires protoc and protoc-gen-go, which are NOT needed to build or
# test this repository -- only to change the schema. Install with:
#   go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
# and a protoc from https://github.com/protocolbuffers/protobuf/releases
#
# CI runs this and fails if the working tree changes, so the committed
# .pb.go files can never drift from the .proto they came from.
set -euo pipefail
cd "$(dirname "$0")"

protoc \
  --proto_path=. \
  --go_out=../uspproto \
  --go_opt=paths=source_relative \
  --go_opt=Musp-record-1-3.proto=acs/internal/usp/uspproto \
  --go_opt=Musp-msg-1-3.proto=acs/internal/usp/uspproto \
  usp-record-1-3.proto usp-msg-1-3.proto

echo "generated:"
ls -1 ../uspproto/*.pb.go
