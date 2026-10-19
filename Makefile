.PHONY: tools proto test test-unit test-integration lint tidy

# Install protoc code generators pinned to module versions.
tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Generate Go from proto/. Requires protoc on PATH.
proto:
	protoc \
		--go_out=. --go_opt=module=github.com/fin/multi-agent-rag \
		--go-grpc_out=. --go-grpc_opt=module=github.com/fin/multi-agent-rag \
		proto/*.proto

# Unit tests only (no Docker required).
test-unit:
	go test -short ./...

# Everything, including testcontainers integration tests (Docker required).
test-integration:
	go test ./...

test: test-integration

lint:
	go vet ./...

tidy:
	go mod tidy
