# syntax = docker/dockerfile:1.0-experimental
FROM golang:1.13-alpine AS build_deps

RUN apk add --no-cache git \
            make bash curl tar

WORKDIR /workspace
ENV GO111MODULE=on

COPY go.mod .
COPY go.sum .

RUN go mod download

FROM build_deps AS build

ARG TEST_ZONE_NAME
ARG SKIP_VERIFY=true

COPY . .

RUN CGO_ENABLED=0 go build -o webhook -ldflags '-w -extldflags "-static"' .

RUN --mount=type=secret,id=api-key,dst=/workspace/testdata/my-custom-solver/api-key.yml \
    ${SKIP_VERIFY} || make verify TEST_ZONE_NAME=${TEST_ZONE_NAME}

FROM alpine

RUN apk add --no-cache ca-certificates

COPY --from=build /workspace/webhook /usr/local/bin/webhook

ENTRYPOINT ["webhook"]
