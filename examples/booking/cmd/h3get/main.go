// Command h3get fetches a URL over HTTP/3 with quic-go's client and prints
// the protocol, the status, the headers, and the body, for trying the
// booking example's -tls mode where curl has no HTTP/3:
//
//	go run ./cmd/h3get -k https://127.0.0.1:8443/health
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/quic-go/quic-go/http3"
)

func main() {
	insecure := flag.Bool("k", false, "accept any certificate, as the example's self-signed one")
	method := flag.String("X", http.MethodGet, "request method")
	bearer := flag.String("bearer", "", "send Authorization: Bearer with this token")
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: h3get [-k] [-X method] [-bearer token] URL")
	}
	tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure}}
	defer tr.Close()
	req, err := http.NewRequest(*method, flag.Arg(0), nil)
	if err != nil {
		log.Fatal(err)
	}
	if *bearer != "" {
		req.Header.Set("Authorization", "Bearer "+*bearer)
	}
	res, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	fmt.Println(res.Proto, res.Status)
	res.Header.Write(os.Stdout)
	fmt.Println()
	io.Copy(os.Stdout, res.Body)
	fmt.Println()
}
