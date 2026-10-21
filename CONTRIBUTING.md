# Contributing to Skybreaker

Skybreaker is maintained by a single developer. Contributions are welcome and appreciated.

## How to Contribute

1. **Bug reports and feature requests** - Open a [GitHub Issue](https://github.com/zaneGittins/skybreaker/issues).
   Security vulnerabilities go through private reporting instead; see [SECURITY.md](SECURITY.md).
2. **Code contributions** - Fork the repo, create a branch, and open a pull request.
3. **Documentation** - Improvements to docs are always welcome.

## Development Setup

```bash
# Clone the repo
git clone https://github.com/zaneGittins/skybreaker.git

# Build and test (Go 1.27.2 or newer; node runs the UI's JS tests if it's installed)
make

# Work on the UI with scripted agents, no sandbox needed
SKYBREAKER_UIDEV=127.0.0.1:8099 go test -run TestUIDev -timeout 0 ./internal/control/controld/
```

More in [docs/development.md](docs/development.md).

## Pull Request Guidelines

- Keep PRs focused on a single change.
- Include a clear description of what and why.
- Work test-first: a failing test, then the change. Security-boundary packages (the broker, sandboxes, auth) keep hostile cases: traversal, encoded separators, replay, foreign credentials, rebinding, protocol violations.
- Ensure `go vet`, `gofmt` and the tests pass: `go test -race ./...`, `go test -tags nodeps ./...`, and for installer changes `(cd setup && go vet ./... && go test -race ./...)`.
- Sign the [CLA](CLA.md) by commenting on your PR: *"I have read the CLA Document and I hereby sign the CLA"*

## Code Style

- Go standard formatting (`gofmt`).
- Concise comments.
- The security rules in [docs/security.md](docs/security.md) hold for every change: deny by default, no credentials in sandboxes, agent text into the UI only as text.

## License

Your contributions are licensed under the [AGPL-3.0](LICENSE). The [CLA](CLA.md) you sign also lets the maintainer offer them under other terms, such as a commercial license.
