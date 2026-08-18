.PHONY: build test vet staticcheck vuln check clean

build:
	go build -o westward ./cmd/westward

test:
	go test ./... -count=1

vet:
	go vet ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: vet staticcheck test

clean:
	rm -f westward
