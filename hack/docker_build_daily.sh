#!/bin/bash

echo "$TRAVIS_EVENT_TYPE, $TRAVIS_BRANCH"

if [[ "$TRAVIS_EVENT_TYPE" = "cron" && "$TRAVIS_BRANCH" = "master" ]]; then
    echo "building container"
    echo "$DOCKER_PASSWORD" | docker login -u "$DOCKER_USERNAME" --password-stdin;
    export IMG="sonic-net/sonic-k8s-manager:daily" && make docker-build;
    make docker-push;
fi