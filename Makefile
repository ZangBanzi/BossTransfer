.PHONY: dev lint test build docker verify verify-metadata package-docker release

dev:
	go run ./apps/manager

lint:
	go test ./... -run TestDoesNotExist

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -o dist/manager ./apps/manager
	CGO_ENABLED=0 go build -trimpath -o dist/client ./apps/client

docker:
	mkdir -p deploy/docker/bin
	CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -trimpath -ldflags="-s -w" -o deploy/docker/bin/manager ./apps/manager
	CGO_ENABLED=0 GOOS=linux GOARCH=$$(go env GOARCH) go build -trimpath -ldflags="-s -w" -o deploy/docker/bin/client ./apps/client
	docker compose --env-file deploy/docker/.env.example -f deploy/docker/docker-compose.yml build

verify:
	pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/verify-phase1.ps1

verify-metadata:
	pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/verify-release-metadata.ps1

package-docker:
	pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/build-docker-package.ps1 -Architecture amd64
	pwsh -NoProfile -ExecutionPolicy Bypass -File scripts/build-docker-package.ps1 -Architecture arm64

release:
	@echo "Run verify and verify-metadata, tag the clean commit, then run package-docker."
