# SPDX-License-Identifier: BUSL-1.1
#
# Copyright (C) 2025 Two Factor Authentication Service, Inc.
# Licensed under the Business Source License 1.1
# See LICENSE file for full terms

FROM golang:1.24-alpine AS builder
RUN apk add --no-cache git

WORKDIR /go/src/2fas-pass-server

COPY . .
RUN go mod download
# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /go/bin/2fas-pass-server
RUN cd healthcheck && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o /go/bin/healthcheck

# Now copy it into base image.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /go/bin/2fas-pass-server /
COPY --from=builder /go/bin/healthcheck /
CMD ["/2fas-pass-server"]
HEALTHCHECK --interval=5s --timeout=1s --start-period=5s --retries=3 CMD ["/healthcheck"]
