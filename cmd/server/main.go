package main

import (
	"github.com/jiawei-wang-dev/go-realtime-collab-editor/internal/api"
	"log"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	address := ":" + port
	log.Printf("collaborative editor listening on %s", address)
	if err := http.ListenAndServe(address, api.NewServer()); err != nil {
		log.Fatal(err)
	}
}
