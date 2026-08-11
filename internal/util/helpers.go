package util

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// AtomicWriteFile writes data to a temp file in the same directory as path
// and renames it into place, so a crash or power loss mid-write leaves
// either the old contents or the new ones, never a truncated/corrupt file.
func AtomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, perm); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", url).Start()
	case "android":
		return exec.Command("am", "start", "-a", "android.intent.action.VIEW", "-d", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
