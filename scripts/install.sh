#!/usr/bin/env sh
set -eu

if [ "$#" -gt 1 ]; then
  echo 'usage: sh scripts/install.sh [version]' >&2
  exit 2
fi
if ! command -v mise >/dev/null 2>&1; then
  echo 'koala: mise is required on PATH; install and activate it: https://mise.jdx.dev/getting-started.html' >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo 'koala: Go is required to install the CLI; install it with mise use -g go@1.26.9' >&2
  exit 1
fi

go install "github.com/bingyuanng/koala/cmd/koala@${1:-latest}"
