# Koala

Koala is a Go backend framework for protobuf-first APIs served over gRPC and
HTTP/JSON. It generates service registration and transport wiring from protobuf
contracts and annotated Go service factories. The runtime provides request
validation, deadlines, structured logging, OpenTelemetry instrumentation, and
embedded OpenAPI documentation.

The framework is under active development. This repository contains the runtime,
CLI, code generators, and application templates.

## Working on Koala

Install and activate [mise](https://mise.jdx.dev/getting-started.html), then run
these commands from the repository root. The checked-in `mise.toml` pins Go, Buf,
and the protobuf generators.

```sh
mise trust
mise install
go run ./cmd/koala help
```

Run the Go checks with:

```sh
go build ./...
go test ./...
go vet ./...
go test -race ./...
```
