package api

import (
	"encoding/json"
	"fmt"
	"idp-platform/internal/config"
	"idp-platform/models"
	"net/http"
	"os"
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
	checkCmd := exec.Command("helm", "status", req.Name, "-n", config.AppConfig.Namespace)
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
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository="+repo,
		"--set", "image.tag="+tag,
		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
		"--set", "type="+req.Type,
		"--set", "env="+config.AppConfig.Env,
		"--set", "source=manual",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	// wait for pod readiness
	status := "pending"

	for i := 0; i < 15; i++ {
		checkPods := exec.Command("kubectl", "get", "pods", "-n", config.AppConfig.Namespace,
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
	cmd := exec.Command(
		"kubectl", "get", "pods",
		"-n", config.AppConfig.Namespace,
		"-o", "json",
	)

	out, err := cmd.Output()
	if err != nil {
		http.Error(w, "failed to get pods", http.StatusInternalServerError)
		return
	}

	type Pod struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Status struct {
			Phase string `json:"phase"`
		} `json:"status"`
	}

	var parsed struct {
		Items []Pod `json:"items"`
	}

	err = json.Unmarshal(out, &parsed)
	if err != nil {
		http.Error(w, "failed to parse kubectl output", http.StatusInternalServerError)
		return
	}

	serviceMap := make(map[string][]ServiceInfo)

	for _, p := range parsed.Items {
		app := p.Metadata.Labels["app"]
		source := p.Metadata.Labels["source"]

		if app == "" {
			continue
		}

		if source == "" {
			source = "unknown" // fallback safety
		}

		serviceMap[app] = append(serviceMap[app], ServiceInfo{
			Name:   app,
			Pod:    p.Metadata.Name,
			Status: p.Status.Phase,
			URL:    "", // FIX: URL should not store source
		})
	}

	var result []map[string]interface{}

	for app, pods := range serviceMap {
		running := 0
		for _, p := range pods {
			if p.Status == "Running" {
				running++
			}
		}

		source := "unknown"
		if len(pods) > 0 {
			// extract from first pod again (clean way)
			for _, p := range parsed.Items {
				if strings.HasPrefix(p.Metadata.Name, app) {
					if val, ok := p.Metadata.Labels["source"]; ok && val != "" {
						source = val
					}
					break
				}
			}
		}

		result = append(result, map[string]interface{}{
			"name":     app,
			"replicas": len(pods),
			"running":  running,
			"pods":     pods,
			"source":   source,
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

	cmd := exec.Command("helm", "uninstall", name, "-n", config.AppConfig.Namespace)

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
	checkCmd := exec.Command("kubectl", "get", "pod", pod, "-n", config.AppConfig.Namespace)
	if err := checkCmd.Run(); err != nil {
		http.Error(w, "pod not found", http.StatusBadRequest)
		return
	}

	// Check pod status
	statusCmd := exec.Command("kubectl", "get", "pod", pod, "-n", config.AppConfig.Namespace, "-o", "jsonpath={.status.phase}")
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
		"kubectl exec -it "+pod+" -n "+config.AppConfig.Namespace+" -- /bin/sh",
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

	cmd := exec.Command("minikube", "service", name, "-n", config.AppConfig.Namespace, "--url")

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

	cmd := exec.Command("kubectl", "logs", pod, "-n", config.AppConfig.Namespace)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(output)
}

func UpdateServiceHandler(w http.ResponseWriter, r *http.Request) {
	var req models.ServiceContract

	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	if req.Replicas < 1 || req.Replicas > 5 {
		http.Error(w, "replicas must be between 1 and 5", http.StatusBadRequest)
		return
	}

	// check service exists
	checkCmd := exec.Command("helm", "status", req.Name, "-n", config.AppConfig.Namespace)
	if err := checkCmd.Run(); err != nil {
		http.Error(w, "service does not exist", http.StatusBadRequest)
		return
	}

	// detect source
	sourceCmd := exec.Command(
		"kubectl", "get", "pods",
		"-n", config.AppConfig.Namespace,
		"-l", "app="+req.Name,
		"-o", "jsonpath={.items[0].metadata.labels.source}",
	)

	sourceOut, _ := sourceCmd.Output()
	source := strings.TrimSpace(string(sourceOut))

	if source == "" {
		source = "manual"
	}

	var repo, tag string

	if source == "repo" {
		// ✅ FORCE repo image (CRITICAL FIX)
		repo = "idp/" + req.Name
		tag = "latest"
	} else {
		// manual → allow user image
		if !strings.Contains(req.Image, ":") {
			http.Error(w, "image must include tag", http.StatusBadRequest)
			return
		}

		parts := strings.Split(req.Image, ":")
		repo = parts[0]
		tag = parts[1]
	}

	// default type safety
	if req.Type == "" {
		req.Type = "web"
	}

	// upgrade
	cmd := exec.Command(
		"helm", "upgrade", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository="+repo,
		"--set", "image.tag="+tag,
		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
		"--set", "type="+req.Type,
		"--set", "env="+config.AppConfig.Env,
		"--set", "source="+source,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"name":     req.Name,
		"image":    repo + ":" + tag,
		"replicas": req.Replicas,
		"source":   source,
		"status":   "updated",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func MetricsHandler(w http.ResponseWriter, r *http.Request) {
	cmd := exec.Command("kubectl", "top", "pods", "-n", config.AppConfig.Namespace, "--no-headers")

	output, err := cmd.Output()
	if err != nil {
		http.Error(w, "metrics not available (is metrics-server installed?)", 500)
		return
	}

	lines := strings.Split(string(output), "\n")

	type Metric struct {
		Name   string `json:"name"`
		Pod    string `json:"pod"`
		CPU    string `json:"cpu"`
		Memory string `json:"memory"`
	}

	var result []Metric

	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}

		pod := fields[0]
		cpu := fields[1]
		mem := fields[2]

		name := strings.Split(pod, "-")[0]

		result = append(result, Metric{
			Name:   name,
			Pod:    pod,
			CPU:    cpu,
			Memory: mem,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func DeployRepoHandler(w http.ResponseWriter, r *http.Request) {
	type Req struct {
		Name string `json:"name"`
		Repo string `json:"repo"`
		Type string `json:"type"`
	}

	var req Req
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Repo == "" {
		http.Error(w, "name and repo required", http.StatusBadRequest)
		return
	}

	if req.Type == "" {
		req.Type = "web"
	}

	// prevent duplicate
	check := exec.Command("helm", "status", req.Name, "-n", config.AppConfig.Namespace)
	if check.Run() == nil {
		http.Error(w, "service already exists", http.StatusBadRequest)
		return
	}

	// working dir
	workDir := "tmp\\" + req.Name

	// cleanup BEFORE clone
	exec.Command("cmd", "/C", "if exist "+workDir+" rmdir /S /Q "+workDir).Run()

	fmt.Println("Cloning repo:", req.Repo)

	// clone repo
	cmd := exec.Command("git", "clone", req.Repo, workDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, "git clone failed: "+string(out), http.StatusInternalServerError)
		return
	}

	// check Dockerfile
	dockerfilePath := workDir + "\\Dockerfile"
	if _, err := os.Stat(dockerfilePath); os.IsNotExist(err) {
		exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()
		http.Error(w, "Dockerfile must be at repo root", http.StatusBadRequest)
		return
	}

	image := "idp/" + req.Name + ":latest"

	// build image
	fmt.Println("Building image:", image)
	build := exec.Command("docker", "build", "-t", image, workDir)
	buildOut, err := build.CombinedOutput()
	if err != nil {
		exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()
		http.Error(w, "build failed: "+string(buildOut), http.StatusInternalServerError)
		return
	}

	// load into minikube
	fmt.Println("Loading into minikube")
	load := exec.Command("minikube", "image", "load", image)
	loadOut, err := load.CombinedOutput()
	if err != nil {
		exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()
		http.Error(w, "minikube load failed: "+string(loadOut), http.StatusInternalServerError)
		return
	}

	// deploy via helm
	repoName := "idp/" + req.Name
	cmd = exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository="+repoName,
		"--set", "image.tag=latest",
		"--set", "type="+req.Type,
		"--set", "env="+config.AppConfig.Env,
		"--set", "source=repo",
	)

	helmOut, err := cmd.CombinedOutput()
	if err != nil {
		exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()
		http.Error(w, "helm failed: "+string(helmOut), http.StatusInternalServerError)
		return
	}

	// cleanup AFTER success
	exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()

	resp := map[string]string{
		"name":   req.Name,
		"repo":   req.Repo,
		"source": "repo",
		"status": "deployed",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
