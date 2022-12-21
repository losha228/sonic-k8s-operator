package main

import (
	"fmt"

	"github.com/sonic-net/sonic-k8s-operator/pkg/anpclient"
)

func main() {
	TestNgs()
	TestNss()
}

func TestNgs() {
	// test ngs client
	device := "BN8-0101-0333-13T1"
	fmt.Printf("Test ngs ReadDeviceInfo for %s \n", device)
	cfg := anpclient.NewConfiguration(anpclient.SERVICE_NGS)
	client := anpclient.NewNgsClient(cfg, "aznetmeta.trafficmanager.net")
	metadata, err := client.ReadDeviceInfo(nil, device)
	if err != nil {
		fmt.Printf("error: %v", err)
		return
	}

	fmt.Printf("ReadDeviceInfo response: %v\n", metadata)
}

func TestNss() {
	// test nss client
	device := "BN8-0101-0333-13T1"
	fmt.Printf("Test nss QueryLock for %s \n", device)
	cfg := anpclient.NewConfiguration(anpclient.SERVICE_NSS)
	client := anpclient.NewNssClient(cfg, "aznetmeta.trafficmanager.net")

	lock, err := client.QueryLock(nil, device)
	if err != nil {
		fmt.Printf("error: %v", err)
		return
	}

	fmt.Printf("QueryLock response: %v\n", lock)
}
