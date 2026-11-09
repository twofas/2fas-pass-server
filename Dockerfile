# SPDX-License-Identifier: BUSL-1.1
#
# Copyright (C) 2025 Two Factor Authentication Service, Inc.
# Licensed under the Business Source License 1.1
# See LICENSE file for full terms

FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS builder
RUN apk add --no-cache git
WORKDIR /go/src/2fas-pass-server

ARG SOURCE_COMMIT

COPY . .
RUN go mod download

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.SourceCommit=${SOURCE_COMMIT}" -o /go/bin/2fas-pass-server
RUN cd healthcheck && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /go/bin/healthcheck

# Now copy it into base image.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=builder /go/bin/2fas-pass-server /
COPY --from=builder /go/bin/healthcheck /
CMD ["/2fas-pass-server"]
HEALTHCHECK --interval=5s --timeout=1s --start-period=5s --retries=3 CMD ["/healthcheck"]
