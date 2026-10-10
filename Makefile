.PHONY: build run test lint clean

build:
	go build -o baitai ./cmd/baitai

run: build
	./baitai

test:
	go test -race ./...

lint:
	gofmt -l . | (! grep .) && go vet ./...

clean:
	rm -f baitai
