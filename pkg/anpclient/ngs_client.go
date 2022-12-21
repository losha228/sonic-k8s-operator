package anpclient

import (
	"context"
	"crypto/tls"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/gocarina/gocsv"
	"github.com/modern-go/concurrent"
)

var (
	NgsLocalCache = concurrent.NewMap()
	// LOCAL_NGS_PATH = "/etc/sonic-credentials/local-ngs/local-ngs.csv"
	LOCAL_NGS_PATH = "E:/tmp/local-ngs.csv"
)

func init() {
	// local ngs from local file or config map so that we can have test data
	LoadNgsFromLocalFile()
}

type NgsClient struct {
	client      *CommonClient
	NgsEndpoint string `json:"ngsEndpoint,omitempty"`
}

func NewNgsClient(cfg *Configuration, ngsEndpoint string) *NgsClient {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}

	c := &NgsClient{}
	c.client = NewCommonClient(cfg, SERVICE_NGS)

	c.client.cfg.BasePath = fmt.Sprintf("https://%s/netgraph", ngsEndpoint)
	c.NgsEndpoint = ngsEndpoint

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

func (n *NgsClient) ReadDeviceMetadata(ctx context.Context, deviceName string) (*DeviceMetadata, error) {
	resp, err := n.client.Get(ctx, fmt.Sprintf("/ReadDeviceMetadata?hostname=%s", deviceName))
	if err != nil {
		return nil, err
	}

	data, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}

	var metadata DeviceMetadata
	err = xml.Unmarshal(data, &metadata)
	return &metadata, err
}

func (n *NgsClient) ReadDevice(ctx context.Context, deviceName string) (*Device, error) {
	resp, err := n.client.Get(ctx, fmt.Sprintf("/ReadDevice?hostname=%s", deviceName))
	if err != nil {
		return nil, err
	}

	data, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}

	var metadata Device
	err = xml.Unmarshal(data, &metadata)
	return &metadata, err
}

func (n *NgsClient) ReadDeviceInfo(ctx context.Context, deviceName string) (*DeviceInfo, error) {
	value, ok := NgsLocalCache.Load(deviceName)
	if ok && value != nil {
		return value.(*DeviceInfo), nil
	}
	// read device metadata
	metadata, err := n.ReadDeviceMetadata(ctx, deviceName)
	if err != nil {
		return nil, err
	}
	device, err := n.ReadDevice(ctx, deviceName)
	if err != nil {
		return nil, err
	}

	deviceInfo := formDeviceInfo(device, metadata)

	NgsLocalCache.Store(deviceName, &deviceInfo)

	return deviceInfo, nil
}

func LoadNgsFromLocalFile() error {
	// load ngs from config map file
	data, err := ioutil.ReadFile(LOCAL_NGS_PATH)
	if err != nil {
		return err
	}
	var records []*DeviceInfo
	err = gocsv.UnmarshalBytes(data, &records)
	if err != nil {
		return err
	}
	for _, r := range records {
		value, ok := NgsLocalCache.Load(r.DeviceName)
		if ok && value == nil {
			continue
		}

		NgsLocalCache.Store(r.DeviceName, r)
	}
	return nil
}

func formDeviceInfo(device *Device, metadata *DeviceMetadata) *DeviceInfo {
	result := &DeviceInfo{}
	result.HwSku = device.HwSku
	result.ElementType = device.ElementType
	result.DeviceName = device.HostName

	result.Region = metadata.GetProperty("Region")
	result.DcCode = metadata.GetProperty("DcCode")
	result.Group = metadata.GetProperty("Group")

	return result
}
