#!/bin/sh
# Installs the tools the build needs into ./bin.
set -eu

PROTOC_VERSION=25.1

mkdir -p bin
curl -fsSL -o protoc.zip "https://github.com/protocolbuffers/protobuf/releases/download/v${PROTOC_VERSION}/protoc-${PROTOC_VERSION}-linux-x86_64.zip"
unzip -o protoc.zip -d bin/protoc
