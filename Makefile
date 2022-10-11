
up:
	@docker-compose up -d

test: up
	@docker-compose run --rm go go test -v -race ./...
