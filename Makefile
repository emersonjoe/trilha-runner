.PHONY: test vet fmt build

test: vet
	go test ./...

vet:
	test -z "$$(gofmt -l runner driver worktree queue sandbox deployer syncer cmd)"
	go vet ./...

fmt:
	gofmt -w runner driver worktree queue sandbox deployer syncer cmd

build:
	go build -o bin/trilha-runner ./cmd/trilha-runner
