# Build the MCP server binary
FROM golang:1.25.7 AS builder

ARG TARGETARCH
ARG TARGETOS

WORKDIR /project

# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source
RUN go mod download

# Copy the go source
COPY . .

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GO111MODULE=on go build -a -o whatsapp-mcp-server .

# Distroless minimal base image
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /project
COPY --from=builder /project/whatsapp-mcp-server .

USER 65532:65532

# This image is intended for HTTP mode (IS_HTTP=true). Stdio-mode users
# typically run the binary locally as a child of Claude Desktop / Cursor.
EXPOSE 5777

ENTRYPOINT ["/project/whatsapp-mcp-server"]
