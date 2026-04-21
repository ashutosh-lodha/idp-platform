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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type ServiceInfo struct {
	Name   string `json:"name"`
	Pod    string `json:"pod"`
	Status string `json:"status"`
	URL    string `json:"url"`
}

var notifyChans = make(map[chan string]bool)
var notifyMu sync.Mutex

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

	if req.Image == "" {
		http.Error(w, "image required", http.StatusBadRequest)
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

	// ================= IMAGE SPLIT (UPDATED) =================

	var repo, tag string

	if strings.Contains(req.Image, ":") {
		parts := strings.Split(req.Image, ":")
		repo = parts[0]
		tag = parts[1]
	} else {
		// ✅ DEFAULT TO LATEST
		repo = req.Image
		tag = "latest"

		// also normalize for minikube load
		req.Image = repo + ":latest"
	}

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

	// ================= FORCE REDEPLOY =================
	redeployTag := fmt.Sprintf("%d", time.Now().Unix())

	// ================= HELM INSTALL =================

	cmdArgs := []string{
		"install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,

		"--set", "image.repository=" + repo,
		"--set", "image.tag=" + tag,

		// ✅ critical for minikube + refresh
		"--set", "image.pullPolicy=Never",
		"--set", "podAnnotations.redeploy=" + redeployTag,

		"--set", fmt.Sprintf("replicaCount=%d", req.Replicas),
		"--set", "type=" + req.Type,
		"--set", "env=" + config.AppConfig.Env,
		"--set", "source=manual",
	}

	// append env + secrets
	cmdArgs = append(cmdArgs, envArgs...)
	cmdArgs = append(cmdArgs, secretArgs...)

	// ================= LOAD IMAGE INTO MINIKUBE =================

	load := exec.Command("minikube", "image", "load", req.Image)
	load.Stdout = os.Stdout
	load.Stderr = os.Stderr

	if err := load.Run(); err != nil {
		http.Error(w, "failed to load image into minikube", 500)
		return
	}

	// ================= HELM EXEC =================

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
		"image":    repo + ":" + tag, // normalized output
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

	// ---------------- HELM DELETE (UNCHANGED) ----------------
	cmd := exec.Command("helm", "uninstall", name, "-n", config.AppConfig.Namespace)
	out, err := cmd.CombinedOutput()

	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}

	// ---------------- TMP CLEANUP (NEW - SAFE ADDITION) ----------------
	workDir := "tmp/" + name
	winPath := strings.ReplaceAll(workDir, "/", "\\")

	cleanCmd := exec.Command("cmd", "/C", "if exist "+winPath+" rmdir /S /Q "+winPath)
	_ = cleanCmd.Run() // ignore error (don't break existing flow)

	// ---------------- RESPONSE (UNCHANGED) ----------------
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

func RollbackServiceHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	// check exists
	check := exec.Command("helm", "status", name, "-n", config.AppConfig.Namespace)
	if err := check.Run(); err != nil {
		http.Error(w, "service not found", http.StatusBadRequest)
		return
	}

	// rollback to previous revision (0 = previous)
	cmd := exec.Command(
		"helm", "rollback", name, "0",
		"-n", config.AppConfig.Namespace,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		http.Error(w, string(out), http.StatusInternalServerError)
		return
	}

	resp := map[string]string{
		"name":   name,
		"status": "rolled back",
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func ServiceHistoryHandler(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")

	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	cmd := exec.Command(
		"helm", "history", name,
		"-n", config.AppConfig.Namespace,
	)

	out, err := cmd.Output()
	if err != nil {
		http.Error(w, "failed to fetch history", http.StatusInternalServerError)
		return
	}

	lines := strings.Split(string(out), "\n")

	var result []map[string]string

	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}

		fields := strings.Fields(line)

		// Minimum fields required before description
		if len(fields) < 7 {
			continue
		}

		revision := fields[0]

		// UPDATED = 5 fields
		updated := strings.Join(fields[1:6], " ")

		status := fields[6]

		chart := ""
		appVersion := ""
		description := ""

		if len(fields) > 7 {
			chart = fields[7]
		}
		if len(fields) > 8 {
			appVersion = fields[8]
		}
		if len(fields) > 9 {
			description = strings.Join(fields[9:], " ")
		}

		result = append(result, map[string]string{
			"revision":    revision,
			"updated":     updated,
			"status":      status,
			"chart":       chart,
			"appVersion":  appVersion,
			"description": description,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func EventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan string, 8)
	notifyMu.Lock()
	notifyChans[ch] = true
	notifyMu.Unlock()
	defer func() { notifyMu.Lock(); delete(notifyChans, ch); notifyMu.Unlock() }()

	flusher, _ := w.(http.Flusher)
	for {
		select {
		case msg := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func GitHubWebhookHandler(w http.ResponseWriter, r *http.Request) {

	fmt.Println("========== WEBHOOK HIT ==========")
	fmt.Println("METHOD:", r.Method)

	// ✅ respond immediately
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))

	if r.Method != http.MethodPost {
		fmt.Println("IGNORED: not POST")
		return
	}

	// ================= PAYLOAD =================
	type Payload struct {
		Repository struct {
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
	}

	var payload Payload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		fmt.Println("ERROR: invalid payload:", err)
		return
	}

	repoURL := payload.Repository.CloneURL
	fmt.Println("RAW REPO URL:", repoURL)

	// ================= NORMALIZE =================
	normalizedRepo := strings.TrimSuffix(repoURL, ".git")
	fmt.Println("NORMALIZED REPO:", normalizedRepo)

	safeRepo := strings.ReplaceAll(normalizedRepo, "https://", "")
	safeRepo = strings.ReplaceAll(safeRepo, "/", "-")
	safeRepo = strings.ReplaceAll(safeRepo, ":", "-")

	fmt.Println("SAFE REPO LABEL:", safeRepo)

	// ================= FIND SERVICES =================
	cmd := exec.Command(
		"kubectl", "get", "pods",
		"-n", config.AppConfig.Namespace,
		"-l", "repo="+safeRepo,
		"-o", "jsonpath={.items[*].metadata.labels.app}",
	)

	out, err := cmd.Output()
	if err != nil {
		fmt.Println("ERROR: kubectl failed:", err)
		return
	}

	serviceSet := make(map[string]bool)
	for _, s := range strings.Fields(string(out)) {
		serviceSet[s] = true
	}

	if len(serviceSet) == 0 {
		fmt.Println("NO SERVICES FOUND FOR REPO")
		return
	}

	fmt.Println("SERVICES FOUND:", serviceSet)

	notifyMu.Lock()
	for name := range serviceSet {
		for ch := range notifyChans {
			select {
			case ch <- name:
			default:
			}
		}
	}
	notifyMu.Unlock()

	// ================= LOOP SERVICES =================
	for name := range serviceSet {

		go func(serviceName string) {

			fmt.Println("----------")
			fmt.Println("AUTO DEPLOY START:", serviceName)

			projectRoot, _ := os.Getwd()
			workDir := filepath.Join(projectRoot, "tmp", serviceName)
			absWorkDir, _ := filepath.Abs(workDir)

			// CLEAN
			os.RemoveAll(absWorkDir)
			if err := os.MkdirAll(absWorkDir, os.ModePerm); err != nil {
				fmt.Println("ERROR: mkdir failed:", err)
				return
			}

			// ================= CLONE =================
			fmt.Println("STEP: CLONING")
			clone := exec.Command("git", "clone", repoURL, absWorkDir)
			clone.Stdout = os.Stdout
			clone.Stderr = os.Stderr

			if err := clone.Run(); err != nil {
				fmt.Println("ERROR: clone failed:", err)
				return
			}

			// ================= DETECT =================
			repoType := "unknown"

			if _, err := os.Stat(filepath.Join(absWorkDir, "Dockerfile")); err == nil {
				repoType = "docker"
			} else if _, err := os.Stat(filepath.Join(absWorkDir, "package.json")); err == nil {
				repoType = "node"
			} else if _, err := os.Stat(filepath.Join(absWorkDir, "requirements.txt")); err == nil {
				repoType = "python"
			}

			fmt.Println("STEP: DETECTED TYPE:", repoType)

			// ================= DOCKERFILE =================
			dockerfilePath := filepath.Join(absWorkDir, "Dockerfile")

			if repoType != "docker" {

				var content string

				if repoType == "node" {
					content = `FROM node:18
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
EXPOSE 3000
CMD ["npm","start"]`
				}

				if repoType == "python" {
					content = `FROM python:3.10
WORKDIR /app
COPY . .
RUN pip install -r requirements.txt
EXPOSE 5000
CMD ["python","app.py"]`
				}

				if content != "" {
					if err := os.WriteFile(dockerfilePath, []byte(content), 0644); err != nil {
						fmt.Println("ERROR: dockerfile write failed:", err)
						return
					}
				}
			}

			if _, err := os.Stat(dockerfilePath); err != nil {
				fmt.Println("ERROR: Dockerfile missing")
				return
			}

			// ================= BUILD =================
			tag := fmt.Sprintf("%d", time.Now().Unix()) // 🔥 UNIQUE TAG
			image := "idp/" + serviceName + ":" + tag

			fmt.Println("STEP: BUILDING IMAGE:", image)

			build := exec.Command("docker", "build", "--no-cache", "-t", image, ".")
			build.Dir = absWorkDir
			build.Stdout = os.Stdout
			build.Stderr = os.Stderr

			if err := build.Run(); err != nil {
				fmt.Println("ERROR: build failed:", err)
				return
			}

			// ================= LOAD INTO MINIKUBE =================
			fmt.Println("STEP: LOADING IMAGE INTO MINIKUBE")

			load := exec.Command("minikube", "image", "load", image)
			load.Stdout = os.Stdout
			load.Stderr = os.Stderr

			if err := load.Run(); err != nil {
				fmt.Println("ERROR: minikube load failed:", err)
				return
			}

			// ================= PORT =================
			port := "80"
			if repoType == "node" {
				port = "3000"
			} else if repoType == "python" {
				port = "5000"
			}

			// ================= HELM =================
			fmt.Println("STEP: HELM UPGRADE")

			helmCmd := exec.Command(
				"helm", "upgrade", serviceName, "charts/myapp",
				"--install",
				"-n", config.AppConfig.Namespace,
				"--set", "image.repository=idp/"+serviceName,
				"--set", "image.tag="+tag,
				"--set", "image.pullPolicy=Always",
				"--set", "source=repo",
				"--set", "repo="+safeRepo,
				"--set", "service.port="+port,
				"--set", "podAnnotations.redeploy="+tag,
			)

			helmCmd.Stdout = os.Stdout
			helmCmd.Stderr = os.Stderr

			if err := helmCmd.Run(); err != nil {
				fmt.Println("ERROR: helm failed:", err)
				return
			}

			fmt.Println("SUCCESS: UPDATED SERVICE:", serviceName)

		}(name)
	}
}

func DeployRepoHandler(w http.ResponseWriter, r *http.Request) {
	type Req struct {
		Name string `json:"name"`
		Repo string `json:"repo"`
		Type string `json:"type"`
	}

	var req Req
	json.NewDecoder(r.Body).Decode(&req)

	flusher, _ := w.(http.Flusher)

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	log := func(msg string) {
		w.Write([]byte(msg + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
	}

	projectRoot, _ := os.Getwd()
	workDir := filepath.Join(projectRoot, "tmp", req.Name)
	absWorkDir, _ := filepath.Abs(workDir)

	cleanup := func() {
		os.RemoveAll(absWorkDir)
		exec.Command("helm", "uninstall", req.Name, "-n", config.AppConfig.Namespace).Run()
	}

	os.RemoveAll(absWorkDir)
	os.MkdirAll(absWorkDir, os.ModePerm)

	// ================= CLONE =================
	log("STEP:CLONING")
	clone := exec.Command("git", "clone", req.Repo, absWorkDir)
	clone.Stdout = w
	clone.Stderr = w

	if err := clone.Run(); err != nil {
		log("STEP:FAILED:CLONE")
		cleanup()
		return
	}

	// ================= DETECT =================
	repoType := "unknown"

	if _, err := os.Stat(filepath.Join(absWorkDir, "Dockerfile")); err == nil {
		repoType = "docker"
	} else if _, err := os.Stat(filepath.Join(absWorkDir, "package.json")); err == nil {
		repoType = "node"
	} else if _, err := os.Stat(filepath.Join(absWorkDir, "requirements.txt")); err == nil {
		repoType = "python"
	}

	log("STEP:DETECTED:" + repoType)

	if repoType == "unknown" {
		log("STEP:FAILED")
		cleanup()
		return
	}

	// ================= DOCKERFILE =================
	if repoType != "docker" {

		var content string

		if repoType == "node" {
			content = `FROM node:18
WORKDIR /app
COPY package*.json ./
RUN npm install
COPY . .
EXPOSE 3000
CMD ["npm","start"]`
		}

		if repoType == "python" {
			content = `FROM python:3.10
WORKDIR /app
COPY . .
RUN pip install -r requirements.txt
EXPOSE 5000
CMD ["python","app.py"]`
		}

		os.WriteFile(filepath.Join(absWorkDir, "Dockerfile"), []byte(content), 0644)
	}

	// ================= BUILD =================
	log("STEP:BUILDING")

	image := "idp/" + req.Name + ":latest"

	build := exec.Command("docker", "build", "-t", image, ".")
	build.Dir = absWorkDir
	build.Stdout = w
	build.Stderr = w

	if err := build.Run(); err != nil {
		log("STEP:FAILED:BUILD")
		cleanup()
		return
	}

	exec.Command("minikube", "image", "load", image).Run()

	// ================= PORT =================
	port := "80"
	if repoType == "node" {
		port = "3000"
	} else if repoType == "python" {
		port = "5000"
	}

	// ================= SANITIZE REPO =================
	safeRepo := strings.ReplaceAll(req.Repo, "https://", "")
	safeRepo = strings.ReplaceAll(safeRepo, "/", "-")
	safeRepo = strings.ReplaceAll(safeRepo, ":", "-")

	// ================= DEPLOY =================
	log("STEP:DEPLOYING")

	cmd := exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository=idp/"+req.Name,
		"--set", "image.tag=latest",
		"--set", "source=repo",
		"--set", "repo="+safeRepo,
		"--set", "service.port="+port,
	)

	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		log("STEP:FAILED:HELM")
		cleanup()
		return
	}

	log("STEP:READY")
}
