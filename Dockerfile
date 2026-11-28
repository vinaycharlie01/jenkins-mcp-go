# syntax=docker/dockerfile:1
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG GIT_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/vinaycharlie01/jenkins-mcp-go/pkg/version.Version=${VERSION} -X github.com/vinaycharlie01/jenkins-mcp-go/pkg/version.GitCommit=${GIT_COMMIT}" \
    -o /out/jenkins-mcp ./cmd/server

# Runs over stdio: start with `docker run -i` from the MCP client.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/jenkins-mcp /usr/local/bin/jenkins-mcp
ENV LOG_FORMAT=json
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/jenkins-mcp"]
