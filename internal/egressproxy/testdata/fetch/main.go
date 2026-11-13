// Command fetch requests each URL it is given and prints, one line per
// URL, the status and the start of the page, or the error. It uses the
// proxy its environment names, as an app's HTTP client does; with
// -direct it uses none. It stands in for an app in the daemon test.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func main() {
	direct := flag.Bool("direct", false, "connect without a proxy")
	flag.Parse()
	// The test image holds no certificate authorities. The test is about
	// which hosts are reached, not about who they are.
	t := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	if *direct {
		t.Proxy = nil
	}
	client := &http.Client{Transport: t, Timeout: 20 * time.Second}
	for _, u := range flag.Args() {
		resp, err := client.Get(u)
		if err != nil {
			fmt.Printf("%s => error: %v\n", u, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		fmt.Printf("%s => %d %s\n", u, resp.StatusCode, strings.Join(strings.Fields(string(body)), " "))
	}
}
