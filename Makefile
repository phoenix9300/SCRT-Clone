# Hawser build
BIN := hawser

.PHONY: all build web go test clean dev

all: build

build: web go

web:
	cd web && npm install --no-audit --no-fund && npm run build

go:
	go build -o $(BIN) ./cmd/hawser

test:
	go test ./...
	cd web && npm test

# cross-compiled release binaries (the UI must be built first)
release: web
	GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/hawser-linux-amd64 ./cmd/hawser
	GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/hawser-linux-arm64 ./cmd/hawser
	GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/hawser-darwin-amd64 ./cmd/hawser
	GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/hawser-darwin-arm64 ./cmd/hawser
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/hawser-windows-amd64.exe ./cmd/hawser

clean:
	rm -rf $(BIN) dist internal/webui/dist/app
