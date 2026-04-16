package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"

	"idp-platform/models"
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

	// JSON support
	if r.Method == http.MethodPost {
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
	} else {
		// fallback (old flow)
		req.Name = r.URL.Query().Get("name")
		req.Image = r.URL.Query().Get("image")
		req.Type = "web"
		req.Replicas = 1
		req.Expose = true
	}

	if req.Name == "" || req.Image == "" {
		http.Error(w, "name and image required", http.StatusBadRequest)
		return
	}

	// defaults
	if req.Replicas == 0 {
		req.Replicas = 1
	}

	// 🔥 FIX: split image into repo + tag
	repo := req.Image
	tag := "latest"

	if strings.Contains(req.Image, ":") {
		parts := strings.Split(req.Image, ":")
		repo = parts[0]
		tag = parts[1]
	}

	// delete if exists
	checkCmd := exec.Command("helm", "status", req.Name, "-n", "idp")
	if err := checkCmd.Run(); err == nil {
		exec.Command("helm", "uninstall", req.Name, "-n", "idp").Run()
	}

	// install
	cmd := exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", "idp",
		"--set", "image.repository="+repo,
		"--set", "image.tag="+tag,
		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	resp := map[string]interface{}{
		"name":     req.Name,
		"type":     req.Type,
		"image":    req.Image,
		"replicas": req.Replicas,
		"status":   "provisioned",
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

	cmd := exec.Command(
		"cmd", "/c", "start", "cmd.exe", "/k",
		"kubectl exec -it "+pod+" -n idp -- /bin/sh",
	)

	err := cmd.Start()
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
