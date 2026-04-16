package main

import (
	"log"
	"net/http"

	"idp-platform/api"
)

func main() {
	mux := http.NewServeMux()

	// API routes
	api.RegisterRoutes(mux)

	// Serve frontend
	fs := http.FileServer(http.Dir("web"))
	mux.Handle("/", fs)

	log.Println("Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
