# Koala

Koala is a Go backend framework for protobuf-first APIs served over gRPC and
HTTP/JSON. It generates service registration and transport wiring from protobuf
contracts and annotated Go service factories. The runtime provides request
validation, deadlines, structured logging, OpenTelemetry instrumentation, and
embedded OpenAPI documentation.

The framework is under active development. This repository contains the runtime,
CLI, code generators, and application templates. Notion tracks the design,
implementation status, and remaining release work. Some documentation describes
planned behavior; check its current implementation notes before relying on it.

## Documentation

Notion is the source of truth for project documentation:

- [Project overview](https://www.notion.so/3f62a93ebee08004854ff018cc58406a)
- [Architecture and design](https://www.notion.so/3f62a93ebee081169554f0a64b221644)
- [Implementation plan](https://www.notion.so/3f62a93ebee08168a8d9d589fb983a5c)
- [Implementation phases](https://www.notion.so/3f62a93ebee081449790ebd40bfa4d0f)
- [Configuration and CLI guide](https://www.notion.so/3f62a93ebee0819f83badea55aabffa6)

Start with the configuration and CLI guide to create an application from a
checkout, generate its API code, and run it locally. Keep design and configuration
documentation updates in Notion.

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

See the configuration and CLI guide for generation, consumer workflow, and
release verification commands.
