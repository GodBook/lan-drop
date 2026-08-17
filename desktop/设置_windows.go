package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type desktopSettings struct {
	SelectedIP string `json:"selected_ip"`
	UploadDir  string `json:"upload_dir"`
}

func defaultUploadDirectory() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "Downloads", "LAN_Drop")
	}
	return filepath.Clean("./LAN_Drop_Files")
}

func settingsFilePath() string {
	if configDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(configDir, "LAN Drop", "设置.json")
	}
	return filepath.Join(os.TempDir(), "LAN Drop", "设置.json")
}

func loadDesktopSettings() desktopSettings {
	settings := desktopSettings{UploadDir: defaultUploadDirectory()}
	data, err := os.ReadFile(settingsFilePath())
	if err == nil {
		_ = json.Unmarshal(data, &settings)
	}
	if strings.TrimSpace(settings.UploadDir) == "" {
		settings.UploadDir = defaultUploadDirectory()
	}
	return settings
}

func saveDesktopSettings(settings desktopSettings) error {
	path := settingsFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func chooseUploadDirectory(current string) (string, error) {
	script := `[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; Add-Type -AssemblyName System.Windows.Forms; $dialog = New-Object System.Windows.Forms.FolderBrowserDialog; $dialog.Description = '选择 LAN Drop 接收目录'; $dialog.ShowNewFolderButton = $true; $dialog.SelectedPath = $env:LANDROP_CURRENT_DIR; if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Write($dialog.SelectedPath) }`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-STA", "-Command", script)
	cmd.Env = append(os.Environ(), "LANDROP_CURRENT_DIR="+current)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", errors.New("未选择目录")
	}
	return filepath.Clean(selected), nil
}

func openDirectory(path string) error {
	return exec.Command("explorer.exe", path).Start()
}
