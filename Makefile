SHELL=/bin/bash -o pipefail

.PHONY: build
build:
	go build -o bin/burn-bot main.go

.PHONY: run
run:
	go run main.go
