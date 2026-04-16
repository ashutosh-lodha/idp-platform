package models

type ServiceContract struct {
	Name     string `json:"name"`
	Type     string `json:"type"` // web | api | worker
	Image    string `json:"image"`
	Replicas int    `json:"replicas"`
	Expose   bool   `json:"expose"`
}
