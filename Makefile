BINARY := fnos-mfs

.PHONY: test build check linux-amd64 clean

test:
	go test ./...

build:
	go build -o $(BINARY) .

check: test build

linux-amd64:
	mkdir -p dist
	rm -f dist/$(BINARY)-linux-amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/$(BINARY) .

clean:
	rm -rf $(BINARY) dist
