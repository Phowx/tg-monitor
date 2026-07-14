package config

import "time"

type ServerRuntimeConfig struct {
	DatabasePath       string
	ListenAddr         string
	CheckpointInterval time.Duration
}

func LoadServerRuntimeFromEnv() (ServerRuntimeConfig, error) {
	checkpointInterval, err := durationFromEnv("TG_MONITOR_CHECKPOINT_INTERVAL", 15*time.Second)
	if err != nil {
		return ServerRuntimeConfig{}, err
	}

	config := ServerRuntimeConfig{
		DatabasePath:       envOrDefault("TG_MONITOR_DATABASE_PATH", "/var/lib/tg-monitor/monitor.db"),
		ListenAddr:         envOrDefault("TG_MONITOR_LISTEN_ADDR", "127.0.0.1:8080"),
		CheckpointInterval: checkpointInterval,
	}
	if err := validateListenAddr(config.ListenAddr); err != nil {
		return ServerRuntimeConfig{}, err
	}
	return config, nil
}
