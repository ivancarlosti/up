// Command tls-lab serves TLS with two certificates, one valid and one already
// expired, so the certificate watching can be exercised (manually or from
// `npm run e2e:certificates`) without a public host.
//
//	go run ./tools/tls-lab
//
// Ports: 127.0.0.1:8443 (valid, 20 days) and 127.0.0.1:8444 (expired, 3 days
// ago). Both are self signed, so a monitor with the default verification reports
// them as down: that is the point — a monitor that ignores TLS errors stays up
// while the certificate events keep firing.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"time"
)

// certificate mints a self signed certificate with the given validity.
func certificate(notBefore, notAfter time.Time) tls.Certificate {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatalf("generating the key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "up-lab", Organization: []string{"Up lab"}},
		Issuer:       pkix.Name{CommonName: "Up lab CA"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		log.Fatalf("creating the certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		log.Fatalf("parsing the certificate: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}
}

// serve starts a TLS listener that answers 200 OK on every path.
func serve(addr string, notBefore, notAfter time.Time) {
	server := &http.Server{
		Addr:    addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") }),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate(notBefore, notAfter)},
			MinVersion:   tls.VersionTLS12,
		},
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listening on %s: %v", addr, err)
	}
	fmt.Printf("listening on %s (expires %s, %d days left)\n",
		addr, notAfter.Format("2006-01-02"), int(time.Until(notAfter).Hours()/24))
	go func() {
		if err := server.ServeTLS(listener, "", ""); err != nil {
			log.Fatalf("serving %s: %v", addr, err)
		}
	}()
}

func main() {
	validAddr := flag.String("valid", "127.0.0.1:8443", "address of the TLS server with a valid certificate")
	expiredAddr := flag.String("expired", "127.0.0.1:8444", "address of the TLS server with an expired certificate")
	validDays := flag.Int("valid-days", 20, "days of validity of the first certificate")
	expiredDays := flag.Int("expired-days", 3, "how many days ago the second certificate expired")
	flag.Parse()

	now := time.Now().UTC()
	serve(*validAddr, now.Add(-24*time.Hour), now.Add(time.Duration(*validDays)*24*time.Hour))
	serve(*expiredAddr,
		now.Add(-time.Duration(*expiredDays+90)*24*time.Hour),
		now.Add(-time.Duration(*expiredDays)*24*time.Hour))
	fmt.Println("tls-lab ready")
	select {}
}
