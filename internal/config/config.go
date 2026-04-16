package config

import (
	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Env       string `yaml:"env"`
	Namespace string `yaml:"namespace"`
}

var AppConfig Config

func LoadConfig() error {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		return err
	}

	return yaml.Unmarshal(data, &AppConfig)
}
