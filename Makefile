.PHONY: build test itest demo check install clean

build:
	go build -o clishake ./cmd/clishake

test:
	go test ./... -count=1

itest:
	CLISHAKE_TMUX_ITEST=1 go test ./... -count=1

demo: build
	demo/demo.sh

check:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./... -count=1

install: build
	install ./clishake /usr/local/bin/clishake

clean:
	rm -f clishake
