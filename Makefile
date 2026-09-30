VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PREFIX ?= $(HOME)/.local
.PHONY: build install uninstall test dist checksums clean
build:
	go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/oku ./cmd/oku
install: build
	install -Dm755 bin/oku $(PREFIX)/bin/oku
uninstall:
	rm -f $(PREFIX)/bin/oku
test:
	go vet ./... && go test ./...
dist:
	VERSION=$(VERSION) ./scripts/build.sh
checksums:
	./scripts/checksums.sh dist
clean:
	rm -rf bin dist
