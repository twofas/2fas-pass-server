.PHONY: help

USERID=$(shell id -u)

.PHONY: licenses
licenses:
	go tool go-licenses report github.com/twofas/2fas-pass-server --ignore github.com/twofas/2fas-pass-server --template licenses.tpl > licenses.json
	cat licenses.json | jq 'sort_by(.package)' > tmp.json && mv tmp.json licenses.json

.PHONY: go-lint
go-lint:
	go tool golangci-lint run

.PHONY: go-lint-fix
go-lint-fix:
	go tool golangci-lint run --fix

.PHONY: license-headers-fix
license-headers-fix:
	go tool addlicense -f license_header.txt -c . -ignore 'e2e/js/node_modules/**/*.js' -ignore '.github/**' -ignore '.idea/**' .

.PHONY: check-license-headers
check-license-headers:
	go tool addlicense -check -f license_header.txt -c . -ignore 'e2e/js/node_modules/**/*.js' -ignore '.github/**' -ignore '.idea/**' .

format:
	 go tool golangci-lint fmt
