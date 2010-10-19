// Copyright 2010 Gary Burd
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

// The oauth package implements a subset of the client interface to OAuth as
// defined in RFC 5849.
package oauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"github.com/garyburd/twister/web"
	"http"
	"io"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"fmt"
	"strconv"
)

var noEscape = [256]bool{
	'A': true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true,
	'a': true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true, true,
	'0': true, true, true, true, true, true, true, true, true, true,
	'-': true,
	'.': true,
	'_': true,
	'~': true,
}

// encode encodes string per section 3.6 of the RFC. If double is true, then
// the encoding is applied twice.
func encode(s string, double bool) []byte {
	// Compute size of result.
	m := 3
	if double {
		m = 5
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if noEscape[s[i]] {
			n += 1
		} else {
			n += m
		}
	}

	p := make([]byte, n)

	// Encode it.
	j := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if noEscape[b] {
			p[j] = b
			j += 1
		} else if double {
			p[j] = '%'
			p[j+1] = '2'
			p[j+2] = '5'
			p[j+3] = "0123456789ABCDEF"[b>>4]
			p[j+4] = "0123456789ABCDEF"[b&15]
			j += 5
		} else {
			p[j] = '%'
			p[j+1] = "0123456789ABCDEF"[b>>4]
			p[j+2] = "0123456789ABCDEF"[b&15]
			j += 3
		}
	}
	return p
}

// Array of key value pairs used for sorting parameters.
type keyValueArray []struct {
	key, value []byte
}

func (p keyValueArray) Len() int {
	return len(p)
}

func (p keyValueArray) Swap(i, j int) {
	p[i], p[j] = p[j], p[i]
}

func (p keyValueArray) Less(i, j int) bool {
	sgn := bytes.Compare(p[i].key, p[j].key)
	if sgn == 0 {
		sgn = bytes.Compare(p[i].value, p[j].value)
	}
	return sgn < 0
}

// writeBaseString writes method, url, and param to w using the OAuth signature
// base string compuation described in section 3.4.1 of the RFC.
func writeBaseString(w io.Writer, method string, url string, param web.StringsMap) {
	// Method
	w.Write(encode(strings.ToUpper(method), false))
	w.Write([]byte{'&'})

	// URL
	parsedURL, _ := http.ParseURL(url)
	w.Write(encode(strings.ToLower(parsedURL.Scheme), false))
	w.Write(encode("://", false))
	w.Write(encode(strings.ToLower(parsedURL.Host), false))
	w.Write(encode(parsedURL.Path, false))
	w.Write([]byte{'&'})

	// Create sorted array of encoded paramters. Parameter keys and values are
	// double encoded in a single step. This is safe to do because double
	// encoding does not change the sort order.
	n := 0
	for _, values := range param {
		n += len(values)
	}

	p := make(keyValueArray, n)
	i := 0
	for key, values := range param {
		encodedKey := encode(key, true)
		for _, value := range values {
			p[i].key = encodedKey
			p[i].value = encode(value, true)
			i += 1
		}
	}
	sort.Sort(p)

	// Write the parameters.
	encodedAmp := encode("&", false)
	encodedEqual := encode("=", false)
	sep := false
	for _, kv := range p {
		if sep {
			w.Write(encodedAmp)
		} else {
			sep = true
		}
		w.Write(kv.key)
		w.Write(encodedEqual)
		w.Write(kv.value)
	}
}

// signature returns the OAuth signature as described in section 3.4 of the RFC.
func signature(clientCredentials *Credentials, credentials *Credentials, method, url string, param web.StringsMap) string {
	var key bytes.Buffer

	key.Write(encode(clientCredentials.Secret, false))
	key.WriteByte('&')
	if credentials != nil {
		key.Write(encode(credentials.Secret, false))
	}

	h := hmac.NewSHA1(key.Bytes())
	writeBaseString(h, method, url, param)
	sum := h.Sum()

	encodedSum := make([]byte, base64.StdEncoding.EncodedLen(len(sum)))
	base64.StdEncoding.Encode(encodedSum, sum)
	return string(encodedSum)
}

var (
	nonceLock    sync.Mutex
	nonceCounter uint64
)

// nonce returns a unique string.
func nonce() string {
	nonceLock.Lock()
	defer nonceLock.Unlock()
	if nonceCounter == 0 {
		binary.Read(rand.Reader, binary.BigEndian, &nonceCounter)
	}
	nonceCounter++
	return strconv.Uitob64(nonceCounter, 16)
}

// Client represents an OAuth client.
type Client struct {
	Credentials                   Credentials
	TemporaryCredentialRequestURI string // Also known as request token URL
	ResourceOwnerAuthorizationURI string // Also known as authorization URL
	TokenRequestURI               string // Alos known as request token URL
}

// Credentials represents client, temporary and token credentials.
type Credentials struct {
	Token  string
	Secret string
}

// SignParam adds an OAuth signature to param.
func (c *Client) SignParam(credentials *Credentials, method, url string, param web.StringsMap) {
	param.Set("oauth_consumer_key", c.Credentials.Token)
	param.Set("oauth_signature_method", "HMAC-SHA1")
	param.Set("oauth_timestamp", strconv.Itoa64(time.Seconds()))
	param.Set("oauth_nonce", nonce())
	param.Set("oauth_version", "1.0")
	if credentials != nil {
		param.Set("oauth_token", credentials.Token)
	}
	param.Set("oauth_signature", signature(&c.Credentials, credentials, method, url, param))
}

func (c *Client) request(credentials *Credentials, url string, param web.StringsMap) (*Credentials, web.StringsMap, os.Error) {
	c.SignParam(credentials, "POST", url, param)
	resp, err := http.PostForm(url, param.StringMap())
	if err != nil {
		return nil, nil, err
	}
	p, err := ioutil.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != 200 {
		return nil, nil, os.NewError(fmt.Sprintf("OAuth server status %d, %s", resp.StatusCode, string(p)))
	}
	m := make(web.StringsMap)
	err = web.ParseUrlEncodedFormBytes(p, m)
	if err != nil {
		return nil, nil, err
	}
	credentials = &Credentials{Token: m.GetDef("oauth_token", ""), Secret: m.GetDef("oauth_token_secret", "")}
	if credentials.Token == "" {
		return nil, nil, os.NewError("No OAuth token in server result")
	}
	if credentials.Secret == "" {
		return nil, nil, os.NewError("No OAuth secret in server result")
	}
	return credentials, m, nil
}

// RequestTemporaryCredentials requests temporary credentials from the server.
func (c *Client) RequestTemporaryCredentials(callbackURL string) (*Credentials, os.Error) {
	m := make(web.StringsMap)
	if callbackURL != "" {
		m.Set("oauth_callback", callbackURL)
	}
	credentials, _, err := c.request(nil, c.TemporaryCredentialRequestURI, m)
	return credentials, err
}

// RequestToken requests token credentials from the server. 
func (c *Client) RequestToken(temporaryCredentials *Credentials) (*Credentials, map[string]string, os.Error) {
	credentials, m, err := c.request(temporaryCredentials, c.TokenRequestURI, make(web.StringsMap))
	if err != nil {
		return nil, nil, err
	}
	return credentials, m.StringMap(), nil
}

// AuthorizationURL returns the full authorization URL.
func (c *Client) AuthorizationURL(temporaryCredentials *Credentials) string {
	return c.ResourceOwnerAuthorizationURI + "?oauth_token=" + string(encode(temporaryCredentials.Token, false))
}
