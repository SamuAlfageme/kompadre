BINARY := kompadre
CMD    := ./cmd/kompadre

VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build clean version

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(CMD)

version:
	@./$(BINARY) --version 2>/dev/null || echo "build first: make build"

clean:
	rm -f $(BINARY)
