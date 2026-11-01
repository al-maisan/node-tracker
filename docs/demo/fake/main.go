//go:build ignore

// Command fake serves the three APIs node-tracker talks to, for the demo GIF:
// bitcoind's getblockchaininfo, Esplora's tip height and Telegram's getMe
// and sendMessage. The node is stuck at -node while the tip is at -tip. Each
// Telegram message is printed to stdout. Loopback only.
//
// Esplora is served over TLS as -tls-host (e.g. mempool.space, mapped to
// 127.0.0.1 by the container), with a throwaway self-signed certificate
// written to -cert for SSL_CERT_FILE. So the reference is not on the node's
// host, as in a real setup.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	node := flag.Int("node", 974079, "node height")
	tip := flag.Int("tip", 974085, "chain tip height")
	addrs := flag.String("listen", "127.0.0.1:8332,127.0.0.1:8081", "comma-separated addresses")
	tlsAddr := flag.String("tls-listen", "127.0.0.1:443", "address of the Esplora API over TLS")
	tlsHost := flag.String("tls-host", "mempool.space", "host name of the TLS certificate")
	certFile := flag.String("cert", "cert.pem", "where to write the certificate, for SSL_CERT_FILE")
	flag.Parse()

	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			fmt.Fprintf(w, `{"result":{"blocks":%d,"headers":%d},"error":null,"id":1}`, *node, *node)
		case r.URL.Path == "/api/blocks/tip/height":
			fmt.Fprint(w, *tip)
		case strings.HasPrefix(r.URL.Path, "/bot") && strings.HasSuffix(r.URL.Path, "/getMe"):
			fmt.Fprint(w, `{"ok":true,"result":{"id":1,"is_bot":true,"username":"orendil_bot"}}`)
		case strings.HasPrefix(r.URL.Path, "/bot") && strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var m struct{ Text string }
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				http.Error(w, `{"ok":false,"description":"bad json"}`, http.StatusBadRequest)
				return
			}
			fmt.Printf("telegram: %q\n", m.Text)
			fmt.Fprint(w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	})
	cert, err := selfSigned(*tlsHost, *certFile)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Addr: *tlsAddr, Handler: h, TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}}
	go func() { log.Fatal(srv.ListenAndServeTLS("", "")) }()
	list := strings.Split(*addrs, ",")
	for _, a := range list[1:] {
		go func() { log.Fatal(http.ListenAndServe(a, h)) }()
	}
	log.Fatal(http.ListenAndServe(list[0], h))
}

// selfSigned makes a one-day certificate for host and writes it to file.
func selfSigned(host, file string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}
