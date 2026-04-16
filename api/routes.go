package api

import "net/http"

func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", HealthHandler)
	mux.HandleFunc("/provision", ProvisionHandler)
	mux.HandleFunc("/services", ListServicesHandler)
	mux.HandleFunc("/delete", DeleteHandler)
	mux.HandleFunc("/exec", ExecHandler)
	mux.HandleFunc("/open", OpenHandler)
	mux.HandleFunc("/logs", LogsHandler)
	mux.HandleFunc("/service", UpdateServiceHandler)
}
