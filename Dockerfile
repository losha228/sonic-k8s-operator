# Build the manager binaries
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.18 as builder

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Copy the go source
COPY main.go main.go
COPY apis/ apis/
COPY pkg/ pkg/
COPY vendor/ vendor/

# Build
RUN CGO_ENABLED=0 GO111MODULE=on go build -mod=vendor -a -o manager main.go

# Use mariner 2.0 as base image to package the binaries
FROM mcr.microsoft.com/cbl-mariner/base/core:2.0
# This is required by connection
RUN ln -s /usr/bin/* /usr/sbin/ && tdnf update -y \
  && tdnf install -y ca-certificates \
  && rm -rf /var/log/*log

WORKDIR /
COPY --from=builder /workspace/manager .
ENTRYPOINT ["/manager"]