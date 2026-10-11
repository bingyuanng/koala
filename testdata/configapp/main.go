package main

import (
	"fmt"
	"github.com/bingyuanng/koala/config"
)

type Config struct {
	Framework config.Config  `json:"framework"`
	Database  DatabaseConfig `json:"database"`
}

type DatabaseConfig struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

func DefaultConfig() Config {
	return Config{
		Framework: config.DefaultConfig(),
		Database:  DatabaseConfig{Host: "localhost"},
	}
}

func main() {
	cfg := DefaultConfig()
	fmt.Println(cfg.Database.Host, cfg.Framework.Server.HTTP.Port, cfg.Framework.Server.HTTP.Enabled)
}
