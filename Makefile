VERSION ?= 0.0.1
LDFLAGS := -s -w -X main.Version=$(VERSION)
XTERM   := 5.5.0
FIT     := 0.10.0
VENDOR  := web/skybreaker/vendor

.PHONY: all build setup dev test vet deps vendor image clean

all: vet test build setup

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/skybreaker ./cmd/skybreaker

# The installer is its own module (bubbletea), so the server takes none of
# its dependencies. A local build is "dev": it installs the latest release.
setup:
	cd setup && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../bin/skybreaker-setup ./cmd/skybreaker-setup

# No third-party code: JSON configs and loopback dev mode only.
dev:
	go build -tags nodeps -o bin/skybreaker ./cmd/skybreaker

test:
	go test -race ./...
	cd setup && go test -race ./...

vet:
	go vet ./...
	cd setup && go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

# Fetch the two third-party modules (tsnet and YAML).
deps:
	go get tailscale.com/tsnet@latest go.yaml.in/yaml/v3@latest
	go mod tidy

# xterm.js is embedded in the binary; the CSP forbids loading it from a CDN.
vendor:
	mkdir -p $(VENDOR)
	curl -fsSL -o $(VENDOR)/xterm.js     https://cdn.jsdelivr.net/npm/@xterm/xterm@$(XTERM)/lib/xterm.js
	curl -fsSL -o $(VENDOR)/xterm.css    https://cdn.jsdelivr.net/npm/@xterm/xterm@$(XTERM)/css/xterm.css
	curl -fsSL -o $(VENDOR)/addon-fit.js https://cdn.jsdelivr.net/npm/@xterm/addon-fit@$(FIT)/lib/addon-fit.js
	curl -fsSL -o $(VENDOR)/LICENSE      https://cdn.jsdelivr.net/npm/@xterm/xterm@$(XTERM)/LICENSE

image:
	docker build -t skybreaker-sandbox:$(VERSION) images/sandbox

clean:
	rm -rf bin
