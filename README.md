# Koala

Koala is a Go backend framework for protobuf-first APIs served over gRPC and
HTTP/JSON. It generates service registration and transport wiring from protobuf
contracts and annotated Go service factories. The runtime provides request
validation, deadlines, structured logging, OpenTelemetry instrumentation, and
embedded OpenAPI documentation.

The latest stable release is
[v1.0.0](https://github.com/bingyuanng/koala/releases/tag/v1.0.0).

## Prerequisites

- Install and activate [mise](https://mise.jdx.dev/getting-started.html) to manage
  Go, Buf, and the protobuf generators pinned by each project.
- Use Go 1.26.10, the version pinned in this repository.
- The repository is currently private. Source installation and application builds
  require repository access and Go private-module configuration until it is made
  public.
- Generation requires access to the Buf Schema Registry or its cache. The first
  generation can also download Go dependencies.

## Installation

Install Go and the Koala CLI:

```sh
mise use -g go@1.26.10
go install github.com/bingyuanng/koala/cmd/koala@latest
export PATH="$(go env GOPATH)/bin:$PATH"
koala version
```

Release archives for Linux and macOS on amd64 and arm64 are also available on the
[latest release page](https://github.com/bingyuanng/koala/releases/latest) to users
with access. For reproducible installation, replace `@latest` with an explicit
release tag.

## Quickstart

Create and start an application:

```sh
koala new my-api --module example.com/my-api
cd my-api
mise trust
mise install
koala gen
koala start
```

The CLI creates an application-owned health service and pins the matching runtime
version. Generation uses pinned Buf plugins.

In another terminal, check the running application:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Both return a JSON status of `ok` once the application is ready. Open
[the API viewer](http://127.0.0.1:8080/docs) to inspect the generated HTTP API. The
default listeners use HTTP port 8080 and gRPC port 9090 on loopback. Stop the
application with Ctrl-C.

To add a service, define its protobuf contract under `proto/` and run `koala gen`.
Generation creates its Go implementation shell and registration. Implement the
RPC methods in that shell. Editing only an existing RPC's business logic requires
a rebuild with `koala start`. Contract or factory changes require
generation. Koala supports unary RPCs and standard HTTP verbs with
simple path variables; streaming and wildcard/custom HTTP paths are unsupported.

## Compatibility and upgrades

Use matching CLI and runtime versions. Review the
[release notes](https://github.com/bingyuanng/koala/releases) before upgrading.
Applications own their startup, configuration, dependencies, service
implementations, and protobuf contracts. Koala does not automatically migrate
those files when its templates change. Compare a temporary project created by
the target CLI and merge the relevant template changes manually.

Commit your application before upgrading, update both versions, review the release
notes and tool pins, regenerate, and inspect the diff. Commit generated transport
code, OpenAPI, the bootstrap registry, `.koala/generation.json`, and
`.koala/descriptors.bin` together with the contract changes. Generation overwrites
Koala-owned outputs, including local edits, and removes obsolete owned outputs.
Conflicting unowned files are rejected; edit application source and protobuf
contracts instead of generated files. Run the application's tests and verify both
transports before deploying.

## Contributing

Include relevant tests with behavior changes and describe how you verified them
in your pull request.

### Development setup

Clone the repository using your GitHub credentials, then install the pinned tools:

```sh
git clone git@github.com:bingyuanng/koala.git
cd koala
mise trust
mise install
go run ./cmd/koala help
```

The checked-in `mise.toml` pins Go, Buf, and the protobuf generators.

### Try a local change

To try the current checkout in a separate application, build the development CLI
and explicitly select this checkout as its runtime:

```sh
koala_checkout=$(pwd)
go build -o /tmp/koala ./cmd/koala
cd /tmp
/tmp/koala new my-api --module example.com/my-api --framework-path "$koala_checkout"
cd my-api
mise trust
mise install
/tmp/koala gen
/tmp/koala start
```

Choose an unused project directory. Development CLI builds require either
`--framework-path` or `--framework-version`; release builds accept
`--framework-version` and default to their matching runtime version.

### Checks

Run these checks from the repository root before opening a pull request:

```sh
scripts/check-format.sh
go build ./...
go test ./...
go vet ./...
go test -race ./...
go run ./scripts/check-generation
go run ./scripts/check-workflow
```

The generation and workflow checks require Buf Schema Registry access or cached
schema dependencies. For local generator execution, run
`mise run local-plugins` and pass `--local-plugins .tools/plugins` to each check.

## License

Koala is available under the [MIT License](LICENSE).
