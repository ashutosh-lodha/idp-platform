package api

import "net/http"

func RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/health", HealthHandler)
	mux.HandleFunc("/provision", ProvisionHandler)
	mux.HandleFunc("/services", ListServicesHandler)
	mux.HandleFunc("/delete-pod", DeletePodHandler)
	mux.HandleFunc("/exec", ExecHandler)
	mux.HandleFunc("/logs", LogsHandler)
	mux.HandleFunc("/service", UpdateServiceHandler)
	mux.HandleFunc("/metrics", MetricsHandler)
	mux.HandleFunc("/deploy-repo", DeployRepoHandler)
	mux.HandleFunc("/delete", DeleteServiceHandler)
	//mux.HandleFunc("/deploy-repo-progress", DeployRepoProgressHandler)
	mux.HandleFunc("/restart", RestartServiceHandler)
	mux.HandleFunc("/rollback", RollbackServiceHandler)
	mux.HandleFunc("/history", ServiceHistoryHandler)
	mux.HandleFunc("/webhook/github", GitHubWebhookHandler)
	mux.HandleFunc("/events", EventsHandler)
	mux.HandleFunc("/diagnose", DiagnoseHandler)
}
