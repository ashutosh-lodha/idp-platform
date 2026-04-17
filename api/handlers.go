package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"idp-platform/internal/config"
	"idp-platform/models"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
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

	// ================= GUARDRAILS =================

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

	// ================= IMAGE SPLIT =================

	parts := strings.Split(req.Image, ":")
	repo := parts[0]
	tag := parts[1]

	// ================= ENV SUPPORT =================

	var envArgs []string
	for k, v := range req.Env {
		envArgs = append(envArgs, "--set", fmt.Sprintf("envVars.%s=%s", k, v))
	}

	// ================= SECRET SUPPORT =================

	var secretArgs []string
	for k, v := range req.Secrets {
		secretArgs = append(secretArgs, "--set", fmt.Sprintf("secrets.%s=%s", k, v))
	}

	// ================= HELM INSTALL =================

	cmdArgs := []string{
		"install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository=" + repo,
		"--set", "image.tag=" + tag,
		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
		"--set", "type=" + req.Type,
		"--set", "env=" + config.AppConfig.Env,
		"--set", "source=manual",
	}

	// append env + secrets
	cmdArgs = append(cmdArgs, envArgs...)
	cmdArgs = append(cmdArgs, secretArgs...)

	cmd := exec.Command("helm", cmdArgs...)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	// ================= RESPONSE =================

	resp := map[string]interface{}{
		"name":     req.Name,
		"type":     req.Type,
		"image":    req.Image,
		"replicas": req.Replicas,
		"status":   "deploying",
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

	json.Unmarshal(out, &parsed)

	serviceMap := make(map[string][]ServiceInfo)
	hashMap := make(map[string]string)

	// latest RS hash
	for _, p := range parsed.Items {
		app := p.Metadata.Labels["app"]
		hash := p.Metadata.Labels["pod-template-hash"]

		if app != "" && hash != "" {
			hashMap[app] = hash
		}
	}

	// filter pods
	for _, p := range parsed.Items {
		app := p.Metadata.Labels["app"]
		hash := p.Metadata.Labels["pod-template-hash"]

		if app == "" || hashMap[app] != hash {
			continue
		}

		stateCmd := exec.Command(
			"kubectl", "get", "pod", p.Metadata.Name,
			"-n", config.AppConfig.Namespace,
			"-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}",
		)

		stateOut, _ := stateCmd.Output()
		state := strings.TrimSpace(string(stateOut))

		status := p.Status.Phase
		if state != "" {
			status = state
		}

		serviceMap[app] = append(serviceMap[app], ServiceInfo{
			Name:   app,
			Pod:    p.Metadata.Name,
			Status: status,
		})
	}

	var result []map[string]interface{}

	// SORT services
	var keys []string
	for k := range serviceMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, app := range keys {
		pods := serviceMap[app]

		// SORT pods
		sort.Slice(pods, func(i, j int) bool {
			return pods[i].Pod < pods[j].Pod
		})

		running := 0
		for _, p := range pods {
			if p.Status == "Running" {
				running++
			}
		}

		source := "manual"

		for _, p := range parsed.Items {
			if strings.HasPrefix(p.Metadata.Name, app) {
				if val := p.Metadata.Labels["source"]; val != "" {
					source = val
				}
				break
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

func DeleteServiceHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("helm", "uninstall", name, "-n", config.AppConfig.Namespace)
	out, err := cmd.CombinedOutput()

	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}

	w.Write([]byte("service deleted"))
}

func DeletePodHandler(w http.ResponseWriter, r *http.Request) {
	pod := r.URL.Query().Get("pod")

	if pod == "" {
		http.Error(w, "pod required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command("kubectl", "delete", "pod", pod, "-n", config.AppConfig.Namespace)
	out, err := cmd.CombinedOutput()

	if err != nil {
		http.Error(w, string(out), 500)
		return
	}

	w.Write([]byte("pod deleted"))
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

func LogsHandler(w http.ResponseWriter, r *http.Request) {
	pod := r.URL.Query().Get("pod")
	if pod == "" {
		http.Error(w, "pod required", 400)
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(
		ctx,
		"kubectl", "logs", "-f", pod,
		"-n", config.AppConfig.Namespace,
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "failed to get logs", 500)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "failed to start logs", 500)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	flusher, _ := w.(http.Flusher)

	reader := bufio.NewReader(stdout)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			break
		}

		_, writeErr := w.Write(line)
		if writeErr != nil {
			// 🔥 CLIENT DISCONNECTED → kill process
			cancel()
			break
		}

		flusher.Flush()
	}

	cmd.Wait()
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

	// check exists
	check := exec.Command("helm", "status", req.Name, "-n", config.AppConfig.Namespace)
	if err := check.Run(); err != nil {
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
		// FORCE repo image (no user override)
		repo = "idp/" + req.Name
		tag = "latest"
	} else {
		if req.Image == "" {
			// fetch existing image from cluster
			imgCmd := exec.Command(
				"kubectl", "get", "deployment", req.Name,
				"-n", config.AppConfig.Namespace,
				"-o", "jsonpath={.spec.template.spec.containers[0].image}",
			)

			imgOut, err := imgCmd.Output()
			if err != nil {
				http.Error(w, "failed to get existing image", http.StatusInternalServerError)
				return
			}

			existingImage := strings.TrimSpace(string(imgOut))

			parts := strings.Split(existingImage, ":")
			repo = parts[0]
			if len(parts) > 1 {
				tag = parts[1]
			} else {
				tag = "latest"
			}

		} else {
			if !strings.Contains(req.Image, ":") {
				http.Error(w, "image must include tag", http.StatusBadRequest)
				return
			}

			parts := strings.Split(req.Image, ":")
			repo = parts[0]
			tag = parts[1]
		}
	}

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

	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
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

func DeployRepoProgressHandler(w http.ResponseWriter, r *http.Request) {
	type Req struct {
		Name string `json:"name"`
		Repo string `json:"repo"`
		Type string `json:"type"`
	}

	var req Req
	json.NewDecoder(r.Body).Decode(&req)

	if req.Name == "" || req.Repo == "" {
		http.Error(w, "name and repo required", 400)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")

	flusher, _ := w.(http.Flusher)

	send := func(msg string) {
		w.Write([]byte(msg + "\n"))
		flusher.Flush()
	}

	send("Cloning repo...")

	workDir := "tmp\\" + req.Name
	exec.Command("cmd", "/C", "if exist "+workDir+" rmdir /S /Q "+workDir).Run()

	cmd := exec.Command("git", "clone", req.Repo, workDir)
	if err := cmd.Run(); err != nil {
		send("❌ Clone failed")
		return
	}

	send("Building image...")

	image := "idp/" + req.Name + ":latest"
	build := exec.Command("docker", "build", "-t", image, workDir)
	if err := build.Run(); err != nil {
		send("❌ Build failed")
		return
	}

	send("Loading into cluster...")

	load := exec.Command("minikube", "image", "load", image)
	if err := load.Run(); err != nil {
		send("❌ Load failed")
		return
	}

	send("Deploying...")

	cmd = exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository=idp/"+req.Name,
		"--set", "image.tag=latest",
		"--set", "type="+req.Type,
		"--set", "env="+config.AppConfig.Env,
		"--set", "source=repo",
	)

	if err := cmd.Run(); err != nil {
		send("❌ Deploy failed")
		return
	}

	send("Waiting for pod...")

	for i := 0; i < 20; i++ {
		out, _ := exec.Command(
			"kubectl", "get", "pods",
			"-n", config.AppConfig.Namespace,
			"-l", "app="+req.Name,
			"-o", "jsonpath={.items[*].status.phase}",
		).Output()

		if strings.Contains(string(out), "Running") {
			send("✅ Running")
			break
		}
	}

	exec.Command("cmd", "/C", "rmdir", "/S", "/Q", workDir).Run()
}

func RestartServiceHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", 400)
		return
	}

	cmd := exec.Command(
		"kubectl", "rollout", "restart",
		"deployment", name,
		"-n", config.AppConfig.Namespace,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(output), 500)
		return
	}

	resp := map[string]string{
		"name":   name,
		"status": "restarted",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
