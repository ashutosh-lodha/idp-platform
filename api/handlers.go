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

func DeployRepoHandler(w http.ResponseWriter, r *http.Request) {
	type Req struct {
		Name string `json:"name"`
		Repo string `json:"repo"`
	}
	cwd, _ := os.Getwd()
	fmt.Println("CWD:", cwd)

	var req Req
	json.NewDecoder(r.Body).Decode(&req)

	flusher, _ := w.(http.Flusher)

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(http.StatusOK)

	log := func(msg string) {
		line := msg + "\n"
		w.Write([]byte(line))
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Print(line)
	}

	log("Starting deployment...")

	// ================= PATH =================
	projectRoot, _ := os.Getwd()
	workDir := filepath.Join(projectRoot, "tmp", req.Name)
	absWorkDir, _ := filepath.Abs(workDir)

	log("ABS PATH: " + absWorkDir)

	// ================= CLEAN =================
	os.RemoveAll(absWorkDir)
	os.MkdirAll(absWorkDir, os.ModePerm)

	// ================= CLONE =================
	log("Cloning...")
	clone := exec.Command("git", "clone", req.Repo, absWorkDir)
	clone.Stdout = w
	clone.Stderr = w
	if err := clone.Run(); err != nil {
		log("❌ clone failed")
		return
	}

	// ================= LIST FILES =================
	files, _ := os.ReadDir(absWorkDir)
	for _, f := range files {
		log("FILE: " + f.Name())
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

	log("Detected: " + repoType)

	if repoType == "unknown" {
		log("❌ unsupported repo type")
		return
	}

	// ================= DOCKERFILE =================
	log("STEP: Creating Dockerfile")

	dockerfilePath := filepath.Join(absWorkDir, "Dockerfile")
	log("Dockerfile path: " + dockerfilePath)

	var dockerContent string

	if repoType == "node" {
		dockerContent =
			"FROM node:18\n" +
				"WORKDIR /app\n" +
				"COPY package*.json ./\n" +
				"RUN npm install\n" +
				"COPY . .\n" +
				"EXPOSE 3000\n" +
				`CMD ["npm","start"]`
	} else if repoType == "python" {
		dockerContent =
			"FROM python:3.10\n" +
				"WORKDIR /app\n" +
				"COPY . .\n" +
				"RUN pip install -r requirements.txt\n" +
				"EXPOSE 5000\n" +
				`CMD ["python","app.py"]`
	} else {
		log("Using existing Dockerfile")
	}

	// FORCE WRITE (only if not docker repo)
	if repoType != "docker" {
		err := os.WriteFile(dockerfilePath, []byte(dockerContent), 0644)
		if err != nil {
			log("❌ WRITE FAILED: " + err.Error())
			return
		}
		log("Dockerfile write attempted")
	}

	// VERIFY FILE EXISTS
	files, _ = os.ReadDir(absWorkDir)
	for _, f := range files {
		log("AFTER WRITE FILE: " + f.Name())
	}

	if _, err := os.Stat(dockerfilePath); err != nil {
		log("❌ Dockerfile STILL NOT PRESENT")
		return
	}

	log("✅ Dockerfile EXISTS")

	// VERIFY CONTENT
	data, err := os.ReadFile(dockerfilePath)
	if err != nil {
		log("❌ Cannot read Dockerfile")
		return
	}

	log(fmt.Sprintf("Dockerfile size: %d bytes", len(data)))

	if len(data) < 50 {
		log("❌ Dockerfile corrupted")
		return
	}

	// ================= BUILD =================
	log("Step: Docker build started")

	image := "idp/" + req.Name + ":latest"

	// 🔥 WINDOWS SAFE BUILD
	build := exec.Command(
		"cmd", "/C",
		"cd", absWorkDir, "&&",
		"docker", "build", "-t", image, ".",
	)

	build.Stdout = w
	build.Stderr = w

	if err := build.Run(); err != nil {
		log("❌ build failed")
		return
	}

	log("✅ build success")

	// ================= MINIKUBE =================
	log("Loading image...")
	exec.Command("minikube", "image", "load", image).Run()

	// ================= HELM =================
	log("Deploying...")

	port := "80"
	if repoType == "node" {
		port = "3000"
	} else if repoType == "python" {
		port = "5000"
	}

	cmd := exec.Command(
		"helm", "install", req.Name, "charts/myapp",
		"-n", config.AppConfig.Namespace,
		"--set", "image.repository=idp/"+req.Name,
		"--set", "image.tag=latest",
		"--set", "source=repo",
		"--set", "service.port="+port,
	)

	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Run(); err != nil {
		log("❌ helm install failed")
		return
	}

	log("Waiting for pods...")

	for i := 0; i < 20; i++ {
		out, _ := exec.Command(
			"kubectl", "get", "pods",
			"-n", config.AppConfig.Namespace,
			"-l", "app="+req.Name,
			"-o", "jsonpath={.items[*].status.phase}",
		).Output()

		if strings.Contains(string(out), "Running") {
			log("✅ Running")
			break
		}

		time.Sleep(1 * time.Second)
	}

	log("🎉 Deployment complete")
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

		if len(fields) < 6 {
			continue
		}

		revision := fields[0]

		// full date (5 parts)
		updated := strings.Join(fields[1:6], " ")

		status := fields[6]

		result = append(result, map[string]string{
			"revision": revision,
			"updated":  updated,
			"status":   status,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}
