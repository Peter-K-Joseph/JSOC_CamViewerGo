package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	DataDir  string
	HTTPHost string
	HTTPPort int
	RTSPHost string
	RTSPPort int

	NativeWSKeepaliveInterval float64
	StreamPathPrefix          string
	AdminPassword             string // set via JSOC_PASSWORD; auto-generated if empty
}

func Load() *Config {
	home, _ := os.UserHomeDir()
	dataDir := getEnv("JSOC_DATA_DIR", filepath.Join(home, ".jsoc_camviewer"))

	password := getEnv("JSOC_PASSWORD", "")
	if password == "" {
		password = randomPassword()
		log.Printf("╔══════════════════════════════════════════╗")
		log.Printf("║  JSOC NVR — no JSOC_PASSWORD set         ║")
		log.Printf("║  Generated password: %-20s  ║", password)
		log.Printf("║  Set JSOC_PASSWORD to silence this       ║")
		log.Printf("╚══════════════════════════════════════════╝")
	}

	return &Config{
		DataDir:                   dataDir,
		HTTPHost:                  getEnv("JSOC_HOST", "0.0.0.0"),
		HTTPPort:                  getEnvInt("JSOC_PORT", 8080),
		RTSPHost:                  getEnv("JSOC_RTSP_HOST", "0.0.0.0"),
		RTSPPort:                  getEnvInt("JSOC_RTSP_PORT", 8554),
		NativeWSKeepaliveInterval: getEnvFloat("JSOC_WS_KEEPALIVE_S", 15.0),
		StreamPathPrefix:          getEnv("JSOC_STREAM_PREFIX", "cam"),
		AdminPassword:             password,
	}
}

func randomPassword() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "changeme"
	}
	return hex.EncodeToString(b)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
