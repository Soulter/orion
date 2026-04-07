APP := orion
DIST := dist

.PHONY: build test clean build-all

build:
	go build -o $(DIST)/$(APP) .

test:
	go test ./...

clean:
	rm -rf $(DIST)

build-all:
	mkdir -p $(DIST)
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/$(APP)-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -o $(DIST)/$(APP)-linux-arm64 .
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/$(APP)-darwin-amd64 .
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/$(APP)-darwin-arm64 .
	GOOS=windows GOARCH=amd64 go build -o $(DIST)/$(APP)-windows-amd64.exe .
