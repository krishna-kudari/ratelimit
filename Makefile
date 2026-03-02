.PHONY: test bench lint fmt vet ci clean setup

setup:
	git config core.hooksPath .githooks
	@echo "Git hooks installed from .githooks/"

test:
	go test -race -count=1 ./...

bench:
	go test -bench=. -benchmem -count=1 -run=^$$ ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

ci: fmt vet lint test

clean:
	go clean -testcache
