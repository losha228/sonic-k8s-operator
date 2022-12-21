#!/usr/bin/env bash

go mod vendor
retVal=$?
if [ $retVal -ne 0 ]; then
    exit $retVal
fi

set -e
TMP_DIR=$(mktemp -d)
mkdir -p "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator/pkg/client
cp -r ./{apis,hack,vendor} "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator/

(cd "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator; \
    GOPATH=${TMP_DIR} GO111MODULE=off /bin/bash vendor/k8s.io/code-generator/generate-groups.sh all \
    github.com/sonic-net/sonic-k8s-operator/pkg/client github.com/sonic-net/sonic-k8s-operator/apis "apps:v1alpha1 apps:v1beta1 policy:v1alpha1" -h ./hack/boilerplate.go.txt)

rm -rf ./pkg/client/{clientset,informers,listers}
mv "${TMP_DIR}"/src/github.com/sonic-net/sonic-k8s-operator/pkg/client/* ./pkg/client
