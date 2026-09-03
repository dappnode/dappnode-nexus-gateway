# Contributing to Dappnode Nexus Gateway

Thank you for helping improve Nexus Gateway. Bug reports, documentation fixes
and pull requests are welcome.

## Before opening a pull request

- Search the [existing issues](https://github.com/dappnode/dappnode-nexus-gateway/issues)
  before reporting a new problem.
- Keep changes focused and include tests for behavior changes.
- Do not commit API keys, credentials, `.env` files or generated runtime
  configuration.

## Development

The project requires Go 1.26.4 or newer.

```sh
go test ./...
go build ./apps/gateway
```

Build the container image with:

```sh
docker build -f apps/gateway/Dockerfile -t dappnode-nexus-gateway:local .
```

The Gateway can be started locally with:

```sh
PII_FILTER_ENABLED=false go run ./apps/gateway
```

The health endpoint will be available at `http://localhost:8080/healthz`.
Model listing, authentication and inference require a compatible Nexus Metering
service; router-backed models also require a Nexus Router endpoint.

## Pull requests

Before submitting a pull request:

1. Format changed Go files with `gofmt`.
2. Run `go test ./...`.
3. Build the Gateway container when changing its Dockerfile or runtime
   dependencies.
4. Explain the user-visible behavior and any compatibility considerations in
   the pull request description.
