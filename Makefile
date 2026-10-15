.PHONY: ci test race flaky e2e host report cover vet fmt build conformance

PKGS_DIRS = runner driver worktree queue sandbox deployer syncer internal cmd

# What CI runs. See docs/testing.md.
ci: test race

test: vet
	go test ./...

race:
	go test -race ./...

# Before calling a flaky test fixed: shuffled, three times, with the race detector.
flaky:
	go test -race -shuffle=on -count=3 ./...

e2e:
	go test -count=1 ./cmd/trilha-runner

# The binary end to end and the packages that drive real git: what the
# macOS CI job runs, since maintainers run the runner on macOS.
host:
	go test -count=1 ./cmd/trilha-runner ./runner ./worktree ./syncer

# Passed/failed/skipped, slowest tests and coverage per package as markdown;
# fails on a failed test or coverage below testdata/coverage-floor.txt.
report:
	go test -count=1 -json -coverprofile=coverage.out ./... > test.json || true
	go run ./internal/testreport -json test.json -cover coverage.out -floor testdata/coverage-floor.txt

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet:
	test -z "$$(gofmt -l $(PKGS_DIRS))"
	go vet ./...

fmt:
	gofmt -w $(PKGS_DIRS)

build:
	go build -o bin/trilha-runner ./cmd/trilha-runner

conformance:
	cmp testdata/contracts/execution-v1.json ../trilha-spec/execution/testdata/run-v1.json
	cmp testdata/contracts/execution-v1.schema.json ../trilha-spec/contracts/execution/v1/schema.json
