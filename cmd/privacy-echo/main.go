// privacy-echo is a dedicated, stateless TLS measurement endpoint. Deploy it
// on an owned host separate from the proxy egress, with no HTTP reverse proxy.
package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"prism/internal/api"
	"prism/internal/nodeprivacy"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9443", "TLS listener")
	cert := flag.String("cert", "", "TLS certificate file")
	key := flag.String("key", "", "TLS private key file")
	flag.Parse()
	if *cert == "" || *key == "" {
		log.Fatal("-cert and -key are required")
	}
	server := api.NewListenerServer(http.HandlerFunc(nodeprivacy.EchoHandler))
	server.Addr = *listen
	server.ReadHeaderTimeout = 5 * time.Second
	server.ReadTimeout = 5 * time.Second
	server.WriteTimeout = 5 * time.Second
	server.IdleTimeout = 10 * time.Second
	server.MaxHeaderBytes = 8192
	server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	log.Fatal(server.ListenAndServeTLS(*cert, *key))
}
