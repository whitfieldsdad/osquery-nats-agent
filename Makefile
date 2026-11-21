DIAGRAMS := $(patsubst %.dot,%.png,$(wildcard docs/*.dot))

default: init diagrams

init:
	find . -name "*.sh" -exec chmod +x {} \;

build:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-darwin-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-windows-amd64.exe ./cmd/agent

clean:
	rm -rf bin

diagrams: $(DIAGRAMS)

%.png: %.dot
	dot -Tpng $< -o $@

.PHONY: init diagrams build clean
