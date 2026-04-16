package main

import (
	"idp-platform/api"
	config "idp-platform/internal/config"
	"log"
	"net/http"
)

func main() {
	// Load config
	err := config.LoadConfig()
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()

	// API routes
	api.RegisterRoutes(mux)

	// Serve frontend
	fs := http.FileServer(http.Dir("web"))
	mux.Handle("/", fs)

	log.Println("Server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
