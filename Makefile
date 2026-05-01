BINARY := kompadre
CMD    := ./cmd/kompadre

.PHONY: all build clean

all: build

build:
	go build -o $(BINARY) $(CMD)

clean:
	rm -f $(BINARY)
