package anpclient

import (
	"encoding/xml"
	"strings"
)

// csv:  worker1,uscentral,BN8,dsm05:79437699,LeafRouter,Arista-7050-QX-32S
type DeviceInfo struct {
	DeviceName  string `csv:"DeviceName"`
	Region      string `csv:"Region"`
	DcCode      string `csv:"DcCode"`
	Group       string `csv:"Group"`
	ElementType string `csv:"ElementType"`
	HwSku       string `csv:"HwSku"`
}

type Device struct {
	XMLName     xml.Name `xml:"Device"`
	HostName    string   `xml:"HostName"`
	HwSku       string   `xml:"HwSku"`
	ClusterName string   `xml:"ClusterName"`
	ElementType string   `xml:"ElementType"`
}

type DeviceMetadata struct {
	XMLName    xml.Name   `xml:"DeviceMetadata"`
	Name       string     `xml:"Name"`
	Properties Properties `xml:"Properties"`
}

type Properties struct {
	XMLName    xml.Name         `xml:"Properties"`
	Properties []DeviceProperty `xml:"DeviceProperty"`
}

type DeviceProperty struct {
	XMLName   xml.Name `xml:"DeviceProperty"`
	Name      string   `xml:"Name"`
	Value     string   `xml:"Value"`
	Reference string   `xml:"Reference"`
}

func (d *DeviceMetadata) GetProperty(name string) string {
	if name == "" {
		return ""
	}

	nameLower := strings.ToLower(name)
	for _, p := range d.Properties.Properties {
		if strings.ToLower(p.Name) == nameLower {
			return p.Value
		}
	}

	return ""
}
