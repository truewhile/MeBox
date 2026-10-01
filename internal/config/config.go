// Package config 加载分层配置：默认值、配置文件和环境变量。
//
// 优先级（低 -> 高）:
//  1. 内置默认值
//  2. 工作目录中的 config.yaml（嵌套格式）
//  3. config/*.yaml 分片文件（按模块）
//  4. 以 MEBOX_ 为前缀的环境变量
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

// EnvPrefix 是所有环境变量驱动的覆盖使用的前缀。
const EnvPrefix = "MeBox"

// RuntimeMu 保护运行时热更新配置字段的并发读写：ApplyRuntimeSetting 在
// HTTP goroutine 中写字段，serverManager 的证书轮询等后台协程在无锁读取
// 同一批字段。string 是双字结构，无锁并发读写可读到撕裂的 header。
// 写方在 ApplyRuntimeSetting 内 Lock，读方（cmd/server）在轮询处 RLock。
var RuntimeMu sync.RWMutex

// Load 从默认值 / 文件 / 环境读取配置。
//
// 即使没有文件也始终返回可用的 Config。
func Load() (*Config, error) {
	v := viper.New()
	setDefaults(v)

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("./config")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !asConfigFileNotFound(err, &notFound) {
			return nil, fmt.Errorf("read main config: %w", err)
		}
	}

	// 合并 ./config/*.yaml 下的分片文件。
	if entries, err := os.ReadDir("config"); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
				continue
			}
			s := viper.New()
			s.SetConfigFile(filepath.Join("config", e.Name()))
			if err := s.ReadInConfig(); err != nil {
				// 分片解析失败不能静默吞掉：database.yaml 语法错误会让
				// database.dsn 缺失 → type=auto 静默回退 SQLite，新数据
				// 全部写进一个空库而用户无感知。
				fmt.Fprintf(os.Stderr, "warning: parse config/%s failed: %v\n", e.Name(), err)
			} else {
				v.MergeConfigMap(s.AllSettings())
			}
		}
	}

	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// asConfigFileNotFound 是 errors.As 的小辅助函数，避免在这个短文件中导入 errors。
func asConfigFileNotFound(err error, target *viper.ConfigFileNotFoundError) bool {
	if err == nil {
		return false
	}
	if v, ok := err.(viper.ConfigFileNotFoundError); ok {
		*target = v
		return true
	}
	return false
}
