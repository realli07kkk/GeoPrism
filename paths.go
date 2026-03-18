package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// getUserHomeDir 获取用户主目录。
func getUserHomeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户主目录失败: %w", err)
	}
	if home == "" {
		return "", fmt.Errorf("获取用户主目录失败: 返回空路径")
	}
	return home, nil
}

// getAppRootDir 获取应用根目录。
func getAppRootDir() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".geoprism"), nil
}

// getConfigDir 获取配置目录。
func getConfigDir() (string, error) {
	appRootDir, err := getAppRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(appRootDir, "config"), nil
}

// getIPDBDir 获取离线 IP 库目录。
func getIPDBDir() (string, error) {
	appRootDir, err := getAppRootDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(appRootDir, "ipdb"), nil
}

// ensureConfigDirReady 确保配置目录存在。
func ensureConfigDirReady() (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	return configDir, nil
}
