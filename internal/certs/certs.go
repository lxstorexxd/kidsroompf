// Package certs loads the provisioned TLS pair and generates the self-signed
// fallback, so the public port always comes up even before a real certificate
// has been pushed.
package certs

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"strings"
	"time"
)

// LoadPair parses a PEM certificate chain and private key.
func LoadPair(certPEM, keyPEM string) (*tls.Certificate, error) {
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
		return nil, errors.New("сертификат или ключ пуст")
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("сертификат и ключ не складываются в пару: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("разбор сертификата: %w", err)
	}
	pair.Leaf = leaf
	return &pair, nil
}

// GenerateSelfSigned issues a 10-year certificate for the domain plus every
// local name and address, so the page opens by domain, by hostname, by LAN IP
// and via localhost.
func GenerateSelfSigned(domain string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}

	dnsNames, ips := localIdentities(domain)
	cn := domain
	if cn == "" {
		cn = "kidsroom-agent"
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"kidsroom-agent"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              dnsNames,
		IPAddresses:           ips,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}

	var certBuf, keyBuf bytes.Buffer
	if err := pem.Encode(&certBuf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		return "", "", err
	}
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		return "", "", err
	}
	return certBuf.String(), keyBuf.String(), nil
}

// localIdentities collects the names and addresses the agent answers on.
func localIdentities(domain string) ([]string, []net.IP) {
	names := []string{"localhost"}
	if domain != "" {
		names = append(names, domain)
	}
	if host, err := os.Hostname(); err == nil && host != "" {
		names = append(names, host)
	}

	ips := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ipNet.IP)
		}
	}
	return dedupeStrings(names), dedupeIPs(ips)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

func dedupeIPs(in []net.IP) []net.IP {
	seen := map[string]bool{}
	out := make([]net.IP, 0, len(in))
	for _, ip := range in {
		if seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip)
	}
	return out
}

// Info is the human-readable summary shown in the GUI and the status endpoint.
type Info struct {
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	Hosts     []string  `json:"hosts"`
	NotAfter  time.Time `json:"not_after"`
	DaysLeft  int       `json:"days_left"`
	SelfSign  bool      `json:"self_signed"`
	Available bool      `json:"available"`
}

// Describe summarises a loaded certificate.
func Describe(pair *tls.Certificate) Info {
	if pair == nil || pair.Leaf == nil {
		return Info{}
	}
	leaf := pair.Leaf
	hosts := append([]string(nil), leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		hosts = append(hosts, ip.String())
	}
	return Info{
		Subject:   leaf.Subject.CommonName,
		Issuer:    leaf.Issuer.CommonName,
		Hosts:     hosts,
		NotAfter:  leaf.NotAfter,
		DaysLeft:  int(time.Until(leaf.NotAfter).Hours() / 24),
		SelfSign:  leaf.Subject.CommonName == leaf.Issuer.CommonName,
		Available: true,
	}
}
