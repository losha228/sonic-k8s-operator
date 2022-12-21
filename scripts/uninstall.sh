#!/usr/bin/env bash

kubectl delete ns sonic-k8s-operator-system
kubectl delete clusterrolebinding sonic-k8s-manager-rolebinding
kubectl delete clusterrole sonic-k8s-manager-role
