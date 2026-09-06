.PHONY: all build test clean

BINARY := gemsub

all: build

build:
	go build -tags with_utls -ldflags "-s -w" -o $(BINARY) ./cmd/gemsub

test:
	go test -tags with_utls -count=1 ./...

clean:
	rm -f $(BINARY)
