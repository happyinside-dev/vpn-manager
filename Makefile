SHELL := /bin/bash
.PHONY: build build-web test fmt vet clean

build-web:
	cd web && npm install && npm test && npm run build
	rm -rf internal/api/static/*
	cp -a web/dist/. internal/api/static/

build: build-web
	go build -trimpath -ldflags='-s -w' -o bin/proxy-manager ./cmd/proxy-manager

test:
	go test ./...
	cd web && npm test

fmt:
	gofmt -w $$(find cmd internal -name '*.go')

vet:
	go vet ./...
	cd web && npm run lint

clean:
	rm -rf bin web/dist web/node_modules
