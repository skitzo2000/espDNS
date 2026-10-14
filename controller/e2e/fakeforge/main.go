// fakeforge is the browser tests' stand-in for the project's release forge (serve.sh starts
// it): a release v0.0.9 of stand-in chip images (internal/dist/disttest), signed by a
// throwaway key made at start, served on a GitHub Enterprise server's paths for owner/espdns on 127.0.0.1, and
// that key's public half written to -pub for the controller's -project-pub. Served over TLS
// (the release URL is https only), its certificate written to -cert for the controller's
// SSL_CERT_FILE. Nothing leaves this machine.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/skitzo2000/espdns/controller/internal/dist/disttest"
	"github.com/skitzo2000/espdns/controller/internal/release"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18490", "where to serve")
	pub := flag.String("pub", "", "where to write the project's public key (required)")
	dir := flag.String("dir", "", "where to write the release (required)")
	cert := flag.String("cert", "", "where to write the forge's TLS certificate (required)")
	flag.Parse()
	if *pub == "" || *dir == "" || *cert == "" {
		log.Fatal("-pub, -dir and -cert are required")
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	if err := disttest.Release(*dir, "0.0.9", k); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*pub, release.PublicRaw(k), 0o644); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v3/repos/owner/espdns/releases", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v0.0.9", "draft": false}})
	})
	mux.Handle("GET /owner/espdns/releases/download/v0.0.9/", http.StripPrefix("/owner/espdns/releases/download/v0.0.9/", http.FileServer(http.Dir(*dir))))
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: mux}}
	srv.StartTLS()
	if err := os.WriteFile(*cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
		log.Fatal(err)
	}
	select {}
}
