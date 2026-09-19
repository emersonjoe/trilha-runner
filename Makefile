.PHONY: test vet fmt build conformance

test: vet
	go test ./...

vet:
	test -z "$$(gofmt -l runner driver worktree queue sandbox deployer syncer cmd)"
	go vet ./...

fmt:
	gofmt -w runner driver worktree queue sandbox deployer syncer cmd

build:
	go build -o bin/trilha-runner ./cmd/trilha-runner

conformance:
	cmp testdata/contracts/execution-v1.json ../trilha-spec/execution/testdata/run-v1.json
	cmp testdata/contracts/execution-v1.schema.json ../trilha-spec/contracts/execution/v1/schema.json
