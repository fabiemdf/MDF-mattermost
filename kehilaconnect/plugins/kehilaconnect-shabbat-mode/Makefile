PLUGIN_ID    := com.kehilaconnect.shabbat-mode
PLUGIN_VERSION := $(shell cat plugin.json | python3 -c "import sys,json; print(json.load(sys.stdin)['version'])")
BUNDLE       := $(PLUGIN_ID)-$(PLUGIN_VERSION).tar.gz

.PHONY: all server webapp bundle clean check-style

all: bundle

## server: Compile the Go plugin binary for all three platforms
server:
	@echo "==> Building server binaries..."
	cd server && \
	  GOOS=linux   GOARCH=amd64 go build -trimpath -o dist/plugin-linux-amd64   . && \
	  GOOS=darwin  GOARCH=amd64 go build -trimpath -o dist/plugin-darwin-amd64  . && \
	  GOOS=windows GOARCH=amd64 go build -trimpath -o dist/plugin-windows-amd64.exe .

## webapp: Install JS dependencies and build the webpack bundle
webapp:
	@echo "==> Building webapp bundle..."
	cd webapp && npm install --legacy-peer-deps && npm run build

## bundle: Assemble the installable plugin tarball
bundle: server webapp
	@echo "==> Bundling plugin $(PLUGIN_ID) v$(PLUGIN_VERSION)..."
	mkdir -p dist/$(PLUGIN_ID)/server/dist
	mkdir -p dist/$(PLUGIN_ID)/webapp/dist
	cp  plugin.json                             dist/$(PLUGIN_ID)/
	cp  server/dist/plugin-linux-amd64         dist/$(PLUGIN_ID)/server/dist/
	cp  server/dist/plugin-darwin-amd64        dist/$(PLUGIN_ID)/server/dist/
	cp  server/dist/plugin-windows-amd64.exe   dist/$(PLUGIN_ID)/server/dist/
	cp  webapp/dist/main.js                    dist/$(PLUGIN_ID)/webapp/dist/
	cd dist && tar czf ../$(BUNDLE) $(PLUGIN_ID)
	@echo "==> Plugin bundle: $(BUNDLE)"

## clean: Remove all build artefacts
clean:
	rm -rf dist server/dist webapp/dist webapp/node_modules $(BUNDLE)

## test: Run Go unit tests
test:
	cd server && go test ./...

## lint: Run golangci-lint on the server code
lint:
	cd server && golangci-lint run ./...
