package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// SaveDatabaseConfig updates or creates config.yaml with the specified database configuration.
func SaveDatabaseConfig(dbType, dsn string) error {
	configPath := "config.yaml"
	data := make(map[string]any)

	content, err := os.ReadFile(configPath)
	if err == nil {
		if err := yaml.Unmarshal(content, &data); err != nil {
			data = make(map[string]any)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read config.yaml: %w", err)
	}

	dbSection, ok := data["database"].(map[string]any)
	if !ok {
		dbSection = make(map[string]any)
	}
	dbSection["type"] = dbType
	dbSection["dsn"] = dsn
	data["database"] = dbSection

	out, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal config.yaml: %w", err)
	}

	// 原子写：临时文件 + rename，避免进程崩溃/断电留下截断的 config.yaml
	// （下次启动会硬失败）；DSN 含数据库密码，权限收窄到 0600。
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write config.yaml.tmp: %w", err)
	}
	if err := os.Rename(tmp, configPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config.yaml: %w", err)
	}
	return nil
}
