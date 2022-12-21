package anpclient

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"net/http"
)

var (
	NSS_ExpirationTimeInSeconds        = 86400
	NSS_Attribute_DeviceLifecycleState = "DeviceLifecycleState"
	NSS_Sonic_k8s_Token                = "SonicK8s"
)

type NssResponseCode string

const (
	NSS_OK       NssResponseCode = "Ok"
	LockNotFound NssResponseCode = "LockNotFound"
)

type NssClient struct {
	client      *CommonClient
	NssEndpoint string `json:"nssEndpoint,omitempty"`
}

type NssResponseCodeElement struct {
	XMLName         xml.Name `xml:"NssResponseCode"`
	NssResponseCode string   `xml:",chardata"`
}

type QueryLockResponse struct {
	XMLName xml.Name `xml:"QueryLockResponse"`
	// the time is invalid if lock doesn't exist, need to use string, or time parse will fail
	AcquiringTime string `xml:"AcquiringTime"`
	ResponseCode  string `xml:"ResponseCode"`
	Token         string `xml:"Token"`
}

func NewNssClient(cfg *Configuration, nssEndpoint string) *NssClient {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	c := &NssClient{}
	c.client = NewCommonClient(cfg, SERVICE_NGS)

	c.client.cfg.BasePath = fmt.Sprintf("https://%s/ngsapi", nssEndpoint)
	c.NssEndpoint = nssEndpoint

	/*  use ca bundle if needed
		caCertPool := x509.NewCertPool()
	if len(l.restConfig.caCert) > 0 {
		caCertPool.AppendCertsFromPEM(l.restConfig.caCert)
	}
	*/
	c.client.cfg.HTTPClient = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				RootCAs:            nil,
			},
		},
	}

	return c
}

// Url: https://aznetmeta.trafficmanager.net/ngsapi/AcquireLock?Entity={ENTITY}&Attribute={ATTRIBUTE}&Token={TOKEN}&ExpirationTimeInSeconds={EXPIRY}
// responce:
// <NssResponseCode xmlns="Microsoft.Search.Autopilot.Evolution">Ok</NssResponseCode>
func (n *NssClient) AcquireLock(ctx context.Context, deviceName string) error {
	resp, err := n.client.Get(ctx, fmt.Sprintf("/AcquireLock?Entity=%s&Attribute=%s&Token=%s&ExpirationTimeInSeconds=%d", deviceName, NSS_Attribute_DeviceLifecycleState, NSS_Sonic_k8s_Token, NSS_ExpirationTimeInSeconds))
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("Failed to acquire lock for %s, response code: %d", deviceName, resp.StatusCode)
	}

	data, err := readResponseBody(resp)
	if err != nil {
		return err
	}

	var response NssResponseCodeElement
	err = xml.Unmarshal(data, &response)
	if err != nil {
		return err
	}

	if response.NssResponseCode != string(NSS_OK) {
		return fmt.Errorf("Response code is %s, not %s", response.NssResponseCode, NSS_OK)
	}
	return nil
}

// Url: https://aznetmeta.trafficmanager.net/ngsapi/ReleaseLock?Entity={ENTITY}&Attribute={ATTRIBUTE}&Token={TOKEN}
// response:
// <NssResponseCode xmlns="Microsoft.Search.Autopilot.Evolution">Ok</NssResponseCode>
func (n *NssClient) ReleaseLock(ctx context.Context, deviceName string) error {
	resp, err := n.client.Get(ctx, fmt.Sprintf("/ReleaseLock?Entity=%s&Attribute=%s", deviceName, NSS_Attribute_DeviceLifecycleState))
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("Failed to release lock for %s, response code: %d", deviceName, resp.StatusCode)
	}

	data, err := readResponseBody(resp)
	if err != nil {
		return err
	}

	var response NssResponseCodeElement
	err = xml.Unmarshal(data, &response)
	if err != nil {
		return err
	}

	if response.NssResponseCode != string(NSS_OK) {
		return fmt.Errorf("Response code is %s, not %s", response.NssResponseCode, NSS_OK)
	}
	return nil
}

// https://aznetmeta.trafficmanager.net/ngsapi/QueryLock?Entity={ENTITY}&Attribute={ATTRIBUTE}
// success response
/*
<QueryLockResponse xmlns="Microsoft.Search.Autopilot.Evolution" xmlns:i="http://www.w3.org/2001/XMLSchema-instance">
<AcquiringTime>2022-11-10T22:54:26.9975824-08:00</AcquiringTime>
<ResponseCode>Ok</ResponseCode>
<Token>ScriptingFramework:sel20-0101-0116-05t0:44d05891-2569-4426-8afd-bed1444d2960</Token>
</QueryLockResponse>
*/

// fail response
/*
<QueryLockResponse xmlns="Microsoft.Search.Autopilot.Evolution" xmlns:i="http://www.w3.org/2001/XMLSchema-instance">
<AcquiringTime>0001-01-01T00:00:00</AcquiringTime>
<ResponseCode>LockNotFound</ResponseCode>
<Token i:nil="true"/>
</QueryLockResponse>
*/
func (n *NssClient) QueryLock(ctx context.Context, deviceName string) (*QueryLockResponse, error) {
	resp, err := n.client.Get(ctx, fmt.Sprintf("/QueryLock?Entity=%s&Attribute=%s", deviceName, NSS_Attribute_DeviceLifecycleState))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Failed to query lock for %s, response code: %d", deviceName, resp.StatusCode)
	}

	data, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}

	var response QueryLockResponse
	err = xml.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}
