GO ?= go
LIB := dist/linux/amd64/opencode-zen-pool-v0.1.0.so

.PHONY: build format format-check test race vet abi integration verify clean
build:
	@test "$$($(GO) env GOOS)/$$($(GO) env GOARCH)" = "linux/amd64" || (echo 'Only linux/amd64 is verified by this distribution'; exit 1)
	mkdir -p $(dir $(LIB))
	CGO_ENABLED=1 $(GO) build -trimpath -buildmode=c-shared -ldflags='-s -w' -o $(LIB) ./src
	rm -f $(LIB:.so=.h)
format:
	gofmt -w src integration
format-check:
	@test -z "$$(gofmt -l src integration)" || (gofmt -l src integration; exit 1)
test:
	$(GO) test -count=1 ./src
race:
	$(GO) test -race -count=1 ./src
vet:
	$(GO) vet -tags=integration ./...
abi: build
	python3 scripts/check_abi.py $(LIB)
integration: build
	@test -n "$(ZEN_HOST_BINARY)" || (echo 'Set ZEN_HOST_BINARY to the native-plugin CLIProxyAPI 7.2.158 executable'; exit 1)
	ZEN_PLUGIN_LIBRARY=$(abspath $(LIB)) $(GO) test -race -tags=integration -count=1 -v ./integration
verify: format-check vet test race abi integration
clean:
	rm -rf dist
