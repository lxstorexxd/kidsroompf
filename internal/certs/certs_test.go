package certs

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"
)

func TestGenerateSelfSignedCoversDomainAndLocalhost(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned("nursery.example.ru")
	if err != nil {
		t.Fatal(err)
	}

	pair, err := LoadPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("собственный сертификат не загрузился: %v", err)
	}

	info := Describe(pair)
	if !info.Available || !info.SelfSign {
		t.Errorf("ожидался самоподписанный сертификат, получено %+v", info)
	}
	for _, want := range []string{"nursery.example.ru", "localhost", "127.0.0.1"} {
		if !contains(info.Hosts, want) {
			t.Errorf("в сертификате нет имени %s: %v", want, info.Hosts)
		}
	}
	if info.NotAfter.Before(time.Now().AddDate(1, 0, 0)) {
		t.Errorf("сертификат истекает слишком рано: %s", info.NotAfter)
	}

	// The certificate must actually satisfy a TLS handshake for the domain.
	if err := pair.Leaf.VerifyHostname("nursery.example.ru"); err != nil {
		t.Errorf("имя не проходит проверку: %v", err)
	}
}

// Without a domain the agent still has to answer on localhost, so the page
// comes up before anything is provisioned.
func TestGenerateSelfSignedWithoutDomain(t *testing.T) {
	certPEM, keyPEM, err := GenerateSelfSigned("")
	if err != nil {
		t.Fatal(err)
	}
	pair, err := LoadPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if err := pair.Leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("localhost не покрыт: %v", err)
	}
	if err := pair.Leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("127.0.0.1 не покрыт: %v", err)
	}
}

func TestLoadPairRejectsBadInput(t *testing.T) {
	good, goodKey, err := GenerateSelfSigned("a.example")
	if err != nil {
		t.Fatal(err)
	}
	_, otherKey, err := GenerateSelfSigned("b.example")
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][2]string{
		"пусто":           {"", ""},
		"нет ключа":       {good, ""},
		"мусор":           {"-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----", goodKey},
		"ключ от другого": {good, otherKey},
	}
	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadPair(pair[0], pair[1]); err == nil {
				t.Fatal("ожидалась ошибка, её нет")
			}
		})
	}
}

func TestDescribeEmpty(t *testing.T) {
	if info := Describe(nil); info.Available {
		t.Error("пустой сертификат не должен считаться доступным")
	}
	if info := Describe(&tls.Certificate{}); info.Available {
		t.Error("сертификат без Leaf не должен считаться доступным")
	}
}

func TestLocalIdentitiesSkipLoopbackDuplicates(t *testing.T) {
	names, ips := localIdentities("example.ru")

	if !contains(names, "localhost") || !contains(names, "example.ru") {
		t.Errorf("не хватает имён: %v", names)
	}
	seen := map[string]bool{}
	for _, ip := range ips {
		if seen[ip.String()] {
			t.Errorf("адрес %s продублирован", ip)
		}
		seen[ip.String()] = true
	}
	if !seen[net.IPv4(127, 0, 0, 1).String()] {
		t.Error("нет 127.0.0.1")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}
