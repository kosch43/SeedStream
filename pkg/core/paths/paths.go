package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

func GetDataDir() string {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "/app/data"
	}
	if _, err := os.Stat("config.json"); err == nil {
		return "."
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "seedstream")
		}
	}
	return "."
}
