package main

import (
	"fmt"
	koala "github.com/bingyuanng/koala"
)

type Config struct {
	Server   koala.ServerConfig `json:"server"`
	Database DatabaseConfig     `json:"database"`
}

type DatabaseConfig struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

func DefaultConfig() Config {
	return Config{
		Server:   koala.ServerConfig{HTTP: koala.HTTPConfig{Port: 8080, Enabled: true}},
		Database: DatabaseConfig{Host: "localhost"},
	}
}

func main() {
	cfg := DefaultConfig()
	fmt.Println(cfg.Database.Host, cfg.Server.HTTP.Port, cfg.Server.HTTP.Enabled)
}
