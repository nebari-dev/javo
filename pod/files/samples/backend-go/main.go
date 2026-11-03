// A Go back-end: a standard-library web server that shows the server time.
//
// Started as: go run main.go (it listens on $PORT). The gateway removes the
// app's URL prefix, so the server answers at /; links stay relative.
package main

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		now := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>Hello from Go</title>"+
			"<h1>Hello from Go</h1><p>The server time is %s.</p>"+
			"<p>Edit main.go and restart the app to change this page.</p>", html.EscapeString(now))
	})
	log.Printf("listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
