#!/usr/bin/env bash

go mod vendor
retVal=$?
if [ $retVal -ne 0 ]; then
    exit $retVal
fi

set -e

# following is an example how to generate code
#TMP_DIR=$(mktemp -d)
#mkdir -p "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator
#cp -r ./{apis,hack,vendor} "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator

#(cd "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator; \
#    GOPATH=${TMP_DIR} GO111MODULE=off go run vendor/k8s.io/kube-openapi/cmd/openapi-gen/openapi-gen.go \
#    -O openapi_generated -i ./apis/apps/pub -p github.com/sonic-net/sonic-k8s-operator/apis/apps/pub -h ./hack/boilerplate.go.txt \
#    --report-filename ./violation_exceptions.list)


#cp -f "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator/apis/apps/pub/openapi_generated.go ./apis/apps/pub


