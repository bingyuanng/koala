# Koala

Koala is a Go backend framework for protobuf-first APIs served over gRPC and
HTTP/JSON. It generates service registration and transport wiring from protobuf
contracts and annotated Go service factories. The runtime provides request
validation, deadlines, structured logging, OpenTelemetry instrumentation, and
embedded OpenAPI documentation.

Koala is in prerelease. The published version is
[`v1.0.0-rc.1`](https://github.com/bingyuanng/koala/releases/tag/v1.0.0-rc.1).
APIs, configuration, CLI behavior, templates, and generated output may change
before stable 1.0. Pin an explicit version and review changes before upgrading.

## Quickstart

Install and activate [mise](https://mise.jdx.dev/getting-started.html), then install
Go and the versioned Koala CLI. The repository is currently private, so source
installation requires repository access and Go private-module configuration until
it is made public:

```sh
mise use -g go@1.26.9
go install github.com/bingyuanng/koala/cmd/koala@v1.0.0-rc.1
export PATH="$(go env GOPATH)/bin:$PATH"
koala version
koala new my-api --module example.com/my-api
cd my-api
mise trust
mise install
koala gen
koala start
```

Release archives for Linux and macOS on amd64 and arm64 are also available on the
release page to users with access. Use the explicit prerelease version rather
than `@latest`.

The CLI creates an application-owned health service and pins the matching runtime
version. Generation uses pinned Buf plugins and requires access to the Buf Schema
Registry or its cache. The first generation can download Go dependencies.

In another terminal, check the running application:

```sh
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/readyz
```

Both return a JSON status of `ok` once the application is ready. Open
[the API viewer](http://127.0.0.1:8080/docs) to inspect the generated HTTP API. The
default listeners use HTTP port 8080 and gRPC port 9090 on loopback. Stop the
application with Ctrl-C.

Add a service with `koala new service Accounts`, edit its protobuf contract, run
`koala gen`, and implement its Go methods. Editing only an existing RPC's business
logic requires a rebuild with `koala start`. Contract or factory changes require
generation. The first release supports unary RPCs and standard HTTP verbs with
simple path variables; streaming and wildcard/custom HTTP paths are unsupported.

## Documentation

The maintained documentation lives in Notion:

- [Project overview](https://bingsthought.notion.site/Koala-3f62a93ebee08004854ff018cc58406a)
- [Configuration, CLI, and upgrade guide](https://bingsthought.notion.site/Configuration-and-CLI-guide-3f62a93ebee0819f83badea55aabffa6)
- [Architecture and design](https://bingsthought.notion.site/Architecture-and-design-3f62a93ebee081169554f0a64b221644)
- [Implementation plan](https://bingsthought.notion.site/Implementation-plan-3f62a93ebee08168a8d9d589fb983a5c)
- [Implementation phases](https://bingsthought.notion.site/Implementation-phases-3f62a93ebee081449790ebd40bfa4d0f)

These pages are public and can be read without a Notion account.

## Compatibility and upgrades

Use matching CLI and runtime versions. During prerelease, an upgrade may require
Go API changes, configuration changes, template edits, and regeneration. Existing
applications own their startup, configuration, dependencies, service implementations,
and protobuf contracts. Koala does not automatically migrate those files when its
templates change. Compare a temporary project created by the target CLI and merge
the relevant template changes manually.

Commit your application before upgrading, update both versions, review the release
notes and tool pins, regenerate, and inspect the diff. Commit generated transport
code, OpenAPI, the bootstrap registry, `.koala/generation.json`, and
`.koala/descriptors.bin` together with the contract changes. Generation overwrites
Koala-owned outputs, including local edits, and removes obsolete owned outputs.
Conflicting unowned files are rejected; edit application source and protobuf
contracts instead of generated files. Run the application's tests and verify both
transports before deploying. See the Notion
upgrade guide for the full procedure and rollback guidance.

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

## License

Koala is available under the [MIT License](LICENSE).
