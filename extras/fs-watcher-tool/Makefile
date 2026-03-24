.PHONY: build test vet fmt clean

BINARY := scion-fs-watcher

build:
	go build -buildvcs=false -o $(BINARY) .

test:
	go test -buildvcs=false ./...

vet:
	go vet -buildvcs=false ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY)

ci: fmt vet test build
