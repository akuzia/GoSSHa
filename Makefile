GITHUB_REF_NAME?=devel

up:
	@docker-compose up -d

test: up
	@docker-compose run --rm go go test -v -race ./...

build:
	@go build -ldflags="-X 'main.version=${GITHUB_REF_NAME}'"