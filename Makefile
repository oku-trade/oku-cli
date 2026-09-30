VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREFIX ?= $(HOME)/.local
.PHONY: build install uninstall test dist checksums clean docs docs-check
build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/oku ./cmd/oku
install: build
	install -Dm755 bin/oku $(PREFIX)/bin/oku
uninstall:
	rm -f $(PREFIX)/bin/oku
test:
	go vet ./... && go test ./... && $(MAKE) docs-check
docs:
	go run ./cmd/oku-docs > docs/commands.md
docs-check:
	@tmp=$$(mktemp); trap 'rm -f "$$tmp"' EXIT; go run ./cmd/oku-docs > "$$tmp" && cmp -s docs/commands.md "$$tmp" || { echo 'docs/commands.md is stale; run make docs' >&2; exit 1; }
dist:
	VERSION=$(VERSION) ./scripts/build.sh
checksums:
	./scripts/checksums.sh dist
clean:
	rm -rf bin dist
