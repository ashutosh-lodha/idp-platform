# 🚀 Self-Service Infrastructure Platform (Mini IDP)

A production-style Internal Developer Platform (IDP) that allows users to deploy, manage, and access applications on Kubernetes through a simple dashboard.

---

## 🎯 Features

* Deploy services dynamically (name + image)
* List all running services
* View pod status
* Open service in browser
* Delete services
* Open terminal inside a pod
* Helm-based deployments
* Terraform-managed infrastructure (namespace only)

---

## 🧱 Tech Stack

* **Go** — Backend (API + orchestration)
* **Kubernetes (Minikube)** — Runtime
* **Helm** — Application deployment
* **Terraform** — Infrastructure provisioning
* **Docker** — Container runtime
* **HTML/CSS/JS** — Frontend

---

## 📁 Project Structure

```
idp-platform/
├── cmd/              # Entry point (main server)
├── api/              # HTTP handlers (routes)
├── internal/         # Core logic (future use)
├── models/           # Data structures
├── charts/           # Helm charts
├── terraform/        # Infra (namespace only)
├── web/              # Frontend UI
├── scripts/          # Helper scripts
├── .github/          # CI/CD (future)
└── README.md
```

---

## ⚙️ Prerequisites

Make sure you have installed:

* Go
* Docker
* Minikube
* kubectl
* Helm
* Terraform

---

## 🚀 Setup & Run

### 1. Start Minikube

```
minikube start --driver=docker
```

---

### 2. Setup Namespace via Terraform

```
cd terraform
terraform init
terraform apply
```

---

### 3. Run Backend Server

```
go run cmd/server/main.go
```

---

### 4. Open Dashboard

```
http://localhost:8080
```

---

## 🧪 How to Use

### Deploy Service

* Enter:

  * Service Name (e.g., `myapp`)
  * Image (e.g., `nginx`)
* Click **Deploy**

---

### View Services

* Click **Refresh**
* See:

  * Name
  * Pod
  * Status

---

### Open Service

* Click **Open**
* Opens app via Minikube tunnel

---

### Delete Service

* Click **Delete**
* Removes Helm release

---

### Exec into Pod

* Click **Exec**
* Opens terminal inside container

---

## ⚠️ Important Design Decisions

* All deployments are restricted to **`idp` namespace**
* Helm is used for all application deployments
* Terraform manages only infrastructure (namespace)
* Backend executes system commands (`helm`, `kubectl`, `minikube`)
* UI communicates only with backend APIs

---

## 🔥 Architecture

```
Browser (UI)
    ↓
Go Backend (API Layer)
    ↓
Helm (Deployments)
    ↓
Kubernetes (Pods/Services)
    ↓
Minikube (Local Cluster)
```

---

## 🧠 Key Concepts Demonstrated

* Kubernetes Deployments & Services
* Helm templating and release management
* Terraform state management and import
* Backend orchestration of infra tools
* Service exposure via Minikube
* Pod-level terminal access

---

## ⚠️ Limitations (Current)

* No authentication
* No persistent storage of service URLs
* Minikube tunnels are not cached
* No production ingress (yet)

---

## 🚧 Future Improvements

* URL caching for services
* Helm upgrade support
* Logs viewer
* Multi-namespace support
* Authentication & RBAC
* Deploy to cloud (EKS/GKE)

---
