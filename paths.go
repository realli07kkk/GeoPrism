package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const legacyAppDirName = "GeoPrism"

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

// getLegacyConfigDir 获取旧版配置目录。
func getLegacyConfigDir() (string, error) {
	home, err := getUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", legacyAppDirName, "config"), nil
}

// ensureConfigDirReady 确保新配置目录存在，并按需迁移旧版配置。
func ensureConfigDirReady() (string, error) {
	configDir, err := getConfigDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}

	newConfigPath := filepath.Join(configDir, "providers.json")
	if fileExists(newConfigPath) {
		return configDir, nil
	}

	legacyConfigDir, err := getLegacyConfigDir()
	if err != nil {
		return "", err
	}

	legacyConfigPath := filepath.Join(legacyConfigDir, "providers.json")
	if !fileExists(legacyConfigPath) {
		return configDir, nil
	}

	if err := copyFile(legacyConfigPath, newConfigPath); err != nil {
		return "", fmt.Errorf("迁移旧版 Provider 配置失败: %w", err)
	}

	return configDir, nil
}

// fileExists 判断文件或目录是否存在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// copyFile 复制文件内容。
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	if err := dstFile.Close(); err != nil {
		return err
	}

	return nil
}
