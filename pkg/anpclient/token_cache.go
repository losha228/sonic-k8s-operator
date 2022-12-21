package anpclient

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"strings"
	"sync"
	"time"

	"encoding/xml"
)

var (
	// service name
	SERVICE_NGS       = "NGS"
	SERVICE_NSS       = "NSS"
	SERVICE_SCS       = "SCS"
	DSTS_TOKEN_PREFIX = "http://schemas.microsoft.com/dsts/saml2-bearer "

	// dsts mount path
	NGS_DSTS_TOKEN_PATH = "e:/tmp/ngs-dsts-token.txt"
	//NGS_DSTS_TOKEN_PATH = "/etc/sonic-credentials/ngs-dsts/ngs-dsts-token.txt"
	NSS_DSTS_TOKEN_PATH = "/etc/sonic-credentials/nss-dsts/nss-dsts-token.txt"
	SCS_DSTS_TOKEN_PATH = "/etc/sonic-credentials/scs-dsts/scs-dsts-token.txt"
	Anp_Token_Map       = map[string]string{
		// key: service name
		// value: token path
		SERVICE_NGS: NGS_DSTS_TOKEN_PATH,
		SERVICE_NSS: NSS_DSTS_TOKEN_PATH,
		SERVICE_SCS: SCS_DSTS_TOKEN_PATH,
	}
)

type AnpTokenSource struct {
	ServiceName string
}

// Get token from config map
func (a *AnpTokenSource) getTokenFromLocalFile() ([]byte, error) {
	// read token from local file which is mounted by a secret
	return ioutil.ReadFile(Anp_Token_Map[a.ServiceName])
}

func (a *AnpTokenSource) Token() (*DstsToken, error) {
	data, err := a.getTokenFromLocalFile()
	if err != nil {
		return nil, err
	}
	dstsToken, err := parseDstsToken(string(data))
	if err != nil {
		return nil, fmt.Errorf("Failed to parse token: %v", err)
	}

	return dstsToken, nil
}

type cachingTokenSource struct {
	base   TokenSource
	leeway time.Duration
	sync.RWMutex

	// cache token
	tok *DstsToken
}

type TokenSource interface {
	Token() (*DstsToken, error)
}

func (ts *cachingTokenSource) Token() (*DstsToken, error) {
	// fast path
	ts.RLock()
	tok := ts.tok
	ts.RUnlock()

	if tok != nil && ts.isTokenValid(tok) {
		return tok, nil
	}

	// slow path
	ts.Lock()
	defer ts.Unlock()

	// refresh token
	tok, err := ts.base.Token()
	if err != nil {
		return ts.tok, fmt.Errorf("Unable to rotate token: %v", err)
	}

	ts.tok = tok
	return tok, nil
}

func (ts *cachingTokenSource) ResetTokenOlderThan(t time.Time) error {
	ts.Lock()
	defer ts.Unlock()
	tok, err := ts.base.Token()
	if err != nil {
		return err
	}
	ts.tok = tok
	return nil
}

/*
	DSTS token sample

<?xml version="1.0" encoding="UTF-8"?>
<Assertion>

	<Conditions NotBefore="2022-11-10T02:49:09.756Z" NotOnOrAfter="2022-11-11T02:49:09.756Z">
	  <AudienceRestriction>
	  <Audience>svc://ngs@aznetmeta.trafficmanager.net/</Audience>
	</AudienceRestriction>

</Conditions>
</Assertion>
*/
func (ts *cachingTokenSource) isTokenValid(token *DstsToken) bool {

	now := time.Now().UTC()
	if now.After(token.NotOnOrAfter.Add(-1*ts.leeway)) || now.Before(token.NotBefore) {
		return false
	}

	return true
}

type Assertion struct {
	XMLName    xml.Name   `xml:"Assertion"`
	Conditions Conditions `xml:"Conditions"`
}

type Conditions struct {
	XMLName      xml.Name  `xml:"Conditions"`
	NotBefore    time.Time `xml:"NotBefore,attr"`
	NotOnOrAfter time.Time `xml:"NotOnOrAfter,attr"`
}

type DstsToken struct {
	Token        string
	NotBefore    time.Time
	NotOnOrAfter time.Time
}

func parseDstsToken(token string) (*DstsToken, error) {
	tokenValue := strings.TrimLeft(token, DSTS_TOKEN_PREFIX)
	dcd, err := base64.StdEncoding.DecodeString(tokenValue)
	if err != nil {
		return nil, err
	}

	var tokenDetail Assertion
	err = xml.Unmarshal(dcd, &tokenDetail)
	if err != nil {
		return nil, err
	}

	result := &DstsToken{
		Token:        token,
		NotBefore:    tokenDetail.Conditions.NotBefore,
		NotOnOrAfter: tokenDetail.Conditions.NotOnOrAfter,
	}

	return result, nil
}
