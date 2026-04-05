.PHONY: build test validate generate cover clean

build:
	go build ./...

test:
	go test ./... -count=1

validate:
	go run ./cmd/gocell validate

generate:
	go run ./cmd/gocell generate assembly --id=core-bundle

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

clean:
	rm -f coverage.out
	rm -f core-bundle
