# Koala

A Go backend framework for protobuf-first gRPC and HTTP/JSON APIs. Project inspired by Grabkit, a internal tool used in one of my previous company.

[Releases](https://github.com/bingyuanng/koala/releases)

Define your API in protobuf and implement your services in Go. Koala generates
service registration and transport wiring.

- gRPC and HTTP/JSON from the same protobuf contract
- Request validation and deadlines
- Structured logging and OpenTelemetry instrumentation
- Embedded OpenAPI documentation

## Get started

Install and activate [mise](https://mise.jdx.dev/getting-started.html), then install
Go and the Koala CLI:

```sh
mise use -g go@1.26.10
go install github.com/bingyuanng/koala/cmd/koala@latest
export PATH="$(go env GOPATH)/bin:$PATH"
```

Upgrade the CLI to the latest version:

```sh
koala upgrade
```

`koala update` is an alias. The command requires Go on PATH and write access to
the CLI's installation directory. It replaces the running CLI after a successful
download and build. Existing projects keep their pinned framework dependency.
To upgrade that dependency, run `go get github.com/bingyuanng/koala@latest` and
`go mod tidy` in the project, then run `koala gen` and your tests.

Create and run an API:

```sh
koala new my-api
cd my-api
mise trust
mise install
koala gen
koala start
```

The Go module path defaults to the project name. Use `--module example.com/my-api`
to set a different path.

Open [localhost:8080/docs](http://127.0.0.1:8080/docs) to explore the API, or check
its health in another terminal:

```sh
curl http://127.0.0.1:8080/healthz
```

Add protobuf services under `proto/`, run `koala gen`, and implement the generated
Go service shells.

`mise install` installs the pinned code generators, and `koala gen` runs them
locally. Network access is needed to download tools and dependencies that are
not already cached.

## Contributing

Install the pinned tools with `mise trust` and `mise install`. Include tests with
behavior changes and describe your verification in the pull request. See
[CI](.github/workflows/ci.yaml) for the required checks.

## License

[MIT](LICENSE)
