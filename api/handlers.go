package api

import (
	"encoding/json"
	"fmt"
	"idp-platform/models"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type ServiceInfo struct {
	Name   string `json:"name"`
	Pod    string `json:"pod"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "OK")
}

func ProvisionHandler(w http.ResponseWriter, r *http.Request) {
	var req models.ServiceContract

	// JSON input
	if r.Method == http.MethodPost {
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	} else {
		// fallback
		req.Name = r.URL.Query().Get("name")
		req.Image = r.URL.Query().Get("image")
		req.Type = "web"
		req.Replicas = 1
		req.Expose = true
	}

	// GUARDRAILS

	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	if strings.Contains(req.Name, " ") || req.Name != strings.ToLower(req.Name) {
		http.Error(w, "invalid name (lowercase, no spaces)", http.StatusBadRequest)
		return
	}

	if req.Type == "" {
		req.Type = "web"
	}
	if req.Type != "web" && req.Type != "api" && req.Type != "worker" {
		http.Error(w, "invalid type (web | api | worker)", http.StatusBadRequest)
		return
	}

	if !strings.Contains(req.Image, ":") {
		http.Error(w, "image must include tag (e.g., nginx:latest)", http.StatusBadRequest)
		return
	}

	if req.Replicas < 1 || req.Replicas > 5 {
		http.Error(w, "replicas must be between 1 and 5", http.StatusBadRequest)
		return
	}

	// duplicate prevention
	checkCmd := exec.Command("helm", "status", req.Name, "-n", "idp")
	if err := checkCmd.Run(); err == nil {
		http.Error(w, "service already exists", http.StatusBadRequest)
		return
	}

	// split image
	parts := strings.Split(req.Image, ":")
	repo := parts[0]
	tag := parts[1]

	// install
	cmd := exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", "idp",
		"--set", "image.repository="+repo,
		"--set", "image.tag="+tag,
		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
		"--set", "type="+req.Type,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	// wait for pod readiness
	status := "pending"

	for i := 0; i < 15; i++ {
		checkPods := exec.Command("kubectl", "get", "pods", "-n", "idp",
			"-l", "app="+req.Name,
			"-o", "jsonpath={.items[*].status.phase}")

		out, _ := checkPods.Output()
		podStatus := string(out)

		if strings.Contains(podStatus, "Running") {
			status = "running"
			break
		}
		if strings.Contains(podStatus, "Error") || strings.Contains(podStatus, "CrashLoopBackOff") {
			status = "failed"
			break
		}

		time.Sleep(1 * time.Second)
	}

	resp := map[string]interface{}{
		"name":     req.Name,
		"type":     req.Type,
		"image":    req.Image,
		"replicas": req.Replicas,
		"status":   status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func ListServicesHandler(w http.ResponseWriter, r *http.Request) {
	podCmd := exec.Command("kubectl", "get", "pods", "-n", "idp", "-o", "jsonpath={.items[*].metadata.name}")
	podOut, _ := podCmd.Output()

	pods := strings.Fields(string(podOut))

	var result []ServiceInfo

	for _, pod := range pods {
		name := strings.Split(pod, "-")[0]

		statusCmd := exec.Command("kubectl", "get", "pod", pod, "-n", "idp", "-o", "jsonpath={.status.phase}")
		statusOut, _ := statusCmd.Output()

		result = append(result, ServiceInfo{
			Name:   name,
			Pod:    pod,
			Status: string(statusOut),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func DeleteHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("helm", "uninstall", name, "-n", "idp")

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, fmt.Sprintf("Error: %s\n%s", err.Error(), string(output)), 500)
		return
	}

	fmt.Fprintf(w, "Deleted:\n%s", string(output))
}

func ExecHandler(w http.ResponseWriter, r *http.Request) {
	pod := r.URL.Query().Get("pod")

	if pod == "" {
		http.Error(w, "pod required", http.StatusBadRequest)
		return
	}

	// Check pod exists
	checkCmd := exec.Command("kubectl", "get", "pod", pod, "-n", "idp")
	if err := checkCmd.Run(); err != nil {
		http.Error(w, "pod not found", http.StatusBadRequest)
		return
	}

	// Check pod status
	statusCmd := exec.Command("kubectl", "get", "pod", pod, "-n", "idp", "-o", "jsonpath={.status.phase}")
	statusOut, err := statusCmd.Output()
	if err != nil {
		http.Error(w, "failed to get pod status", 500)
		return
	}

	status := string(statusOut)
	if status != "Running" {
		http.Error(w, "pod is not running", http.StatusBadRequest)
		return
	}

	// Open terminal
	cmd := exec.Command(
		"cmd", "/c", "start", "cmd.exe", "/k",
		"kubectl exec -it "+pod+" -n idp -- /bin/sh",
	)

	err = cmd.Start()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	w.Write([]byte("Terminal opened"))
}

func OpenHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("minikube", "service", name, "-n", "idp", "--url")

	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	buf := make([]byte, 1024)
	n, _ := outPipe.Read(buf)

	url := strings.TrimSpace(string(buf[:n]))

	w.Write([]byte(url))
}

func LogsHandler(w http.ResponseWriter, r *http.Request) {
	pod := r.URL.Query().Get("pod")

	if pod == "" {
		http.Error(w, "pod required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("kubectl", "logs", pod, "-n", "idp")

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(output)
}
