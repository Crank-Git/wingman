package dns

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/Crank-Git/wingman/internal/credentials"
	"github.com/miekg/dns"
)

// TSIGKey configures one HMAC-SHA256 identity. Set it before concurrent use.
type TSIGKey struct {
	Name      string `json:"name"`
	Algorithm string `json:"algorithm"`
	Secret    string `json:"secret"`
}

// LoadTSIGKey returns a validated key from a restricted JSON file.
func LoadTSIGKey(path string) (*TSIGKey, error) {
	data, err := credentials.ReadRestrictedFile(path)
	if err != nil {
		return nil, err
	}
	var key TSIGKey
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&key); err != nil {
		return nil, errors.New("load TSIG key: invalid JSON fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("load TSIG key: trailing JSON data")
	}
	if key.Algorithm == "hmac-sha256" {
		key.Algorithm = dns.HmacSHA256
	}
	if err := key.validate(); err != nil {
		return nil, err
	}
	return &key, nil
}

func (k *TSIGKey) validate() error {
	if _, ok := dns.IsDomainName(k.Name); !ok || k.Name == "." || !dns.IsFqdn(k.Name) || k.Name != dns.CanonicalName(k.Name) || strings.Contains(k.Name, "\\") {
		return errors.New("TSIG key: name must be a canonical FQDN")
	}
	if k.Algorithm != dns.HmacSHA256 {
		return errors.New("TSIG key: algorithm must be hmac-sha256.")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(k.Secret)
	if err != nil || len(raw) == 0 || strings.ContainsAny(k.Secret, "\r\n") {
		return errors.New("TSIG key: secret must contain valid base64")
	}
	return nil
}

func (k *TSIGKey) secrets() map[string]string {
	if k == nil {
		return nil
	}
	return map[string]string{k.Name: k.Secret}
}

func (k *TSIGKey) validateReply(resp *dns.Msg) error {
	t := resp.IsTsig()
	if t == nil {
		return errors.New("DNS authentication: unsigned response")
	}
	if t.Hdr.Name != k.Name || t.Algorithm != k.Algorithm || t.Error != 0 {
		return errors.New("DNS authentication: rejected response signature")
	}
	return nil
}
