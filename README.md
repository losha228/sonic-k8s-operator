# What's sonic-k8s-operator ?
The sonic k8s operator is a k8s controller to orchestrate daemonset update by integrating prechec/postcheck webhooks.

# Key components
- sonic daemonset controller : handle sonic daemonset update
- sonic webhook controller: implement precheck/postcheck hooks

# How to test in local dev
```
go build
 ./sonic-k8s-operator -v 5 --stderrthreshold INFO --namespace default --admission-webhook-enabled=false
 ```

# How to generate admission webhook cert

scripts/webhook-create-signed-cert.sh --service sonic-k8s-webhook-service  --namespace  sonic-k8s-system --secret webhook-certs

# How to create ca bundle
```
cat  validatingwebhook.yaml | ./webhook-patch-ca-bundle.sh > ./validatingwebhook-ca-bundle.yaml
```

# How to create validation webhook
```
kubectl apply -f validatingwebhook-ca-bundle.yaml
```
