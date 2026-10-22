.PHONY: help

USERID=$(shell id -u)

.PHONY: licenses
licenses:
	go tool -modfile=tools/go.mod go-licenses report github.com/twofas/2fas-pass-server --ignore github.com/twofas/2fas-pass-server --template licenses.tpl > licenses.json
	cat licenses.json | jq 'sort_by(.package)' > tmp.json && mv tmp.json licenses.json

.PHONY: go-lint
go-lint:
	go tool -modfile=tools/go.mod golangci-lint run

.PHONY: go-lint-fix
go-lint-fix:
	go tool -modfile=tools/go.mod golangci-lint run --fix

.PHONY: license-headers-fix
license-headers-fix:
	go tool -modfile=tools/go.mod addlicense -f license_header.txt -c . -ignore 'e2e/js/node_modules/**/*.js' -ignore '.github/**' -ignore '.idea/**' .

.PHONY: check-license-headers
check-license-headers:
	go tool -modfile=tools/go.mod addlicense -check -f license_header.txt -c . -ignore 'e2e/js/node_modules/**/*.js' -ignore '.github/**' -ignore '.idea/**' .

format:
	 go tool -modfile=tools/go.mod golangci-lint fmt
