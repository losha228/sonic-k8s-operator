# sonic-k8s-operator

# how to test in local dev
go build
 ./sonic-k8s-operator -v 5 --stderrthreshold INFO --namespace default --admission-webhook-enabled=false

# how to generate admission webhook cert

scripts/webhook-create-signed-cert.sh --service sonic-k8s-webhook-service  --namespace  sonic-k8s-system --secret webhook-certs
