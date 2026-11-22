// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// NotifyDesktop sends one best-effort desktop notification per version. The
// notify policy and the source-only fallback of the apply policy call it; an
// apply run installs the verified update instead of notifying.
func NotifyDesktop(state State) error {
	if IsTestBinary() || !state.UpdateAvailable || state.DowngradeAvailable || state.NeedsSourceBaseline {
		return nil
	}
	tag := strings.TrimSpace(state.LatestTag)
	if state.Channel == "dev" && strings.TrimSpace(state.LatestCommitShort) != "" {
		tag = "main@" + strings.TrimSpace(state.LatestCommitShort)
	} else {
		tag = "v" + strings.TrimPrefix(tag, "v")
	}
	if tag == "" || alreadyNotified(tag) {
		return nil
	}
	message := fmt.Sprintf("New version %s is available (installed v%s).", tag, state.CurrentVersion)
	if state.AssetsChecked && !state.Installable {
		message = fmt.Sprintf("New version %s is available; this channel has no prebuilt artifact. Open the console for update options.", tag)
	}
	delivered, err := dispatchNotification("Rolling Memory", message)
	if err != nil {
		return err
	}
	if delivered {
		markNotified(tag)
	}
	return nil
}

type notifyRecord struct {
	Tag string `json:"tag"`
	At  string `json:"at"`
}

func notifyRecordPath() string {
	return filepath.Join(filepath.Dir(StatePath()), "update-notify.json")
}

func alreadyNotified(tag string) bool {
	raw, err := os.ReadFile(notifyRecordPath())
	if err != nil {
		return false
	}
	var record notifyRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return false
	}
	return strings.TrimSpace(record.Tag) == tag
}

func markNotified(tag string) {
	record := notifyRecord{Tag: tag, At: time.Now().UTC().Format(time.RFC3339)}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return
	}
	path := notifyRecordPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(temporary, path)
}

func dispatchNotification(title, message string) (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		bin, err := exec.LookPath("osascript")
		if err != nil {
			return false, nil
		}
		script := fmt.Sprintf("display notification %q with title %q", message, title)
		if err := exec.Command(bin, "-e", script).Run(); err != nil {
			return false, err
		}
		return true, nil
	case "linux":
		bin, err := exec.LookPath("notify-send")
		if err != nil {
			return false, nil
		}
		if err := exec.Command(bin, title, message).Run(); err != nil {
			return false, err
		}
		return true, nil
	case "windows":
		bin, err := exec.LookPath("powershell")
		if err != nil {
			return false, nil
		}
		safeMessage := strings.ReplaceAll(message, "'", "''")
		script := "Add-Type -AssemblyName System.Windows.Forms; " +
			"$n=New-Object System.Windows.Forms.NotifyIcon; " +
			"$n.Icon=[System.Drawing.SystemIcons]::Information; " +
			"$n.Visible=$true; " +
			"$n.ShowBalloonTip(10000,'Rolling Memory','" + safeMessage + "','Info'); " +
			"Start-Sleep -Seconds 2; $n.Dispose()"
		if err := exec.Command(bin, "-NoProfile", "-WindowStyle", "Hidden", "-Command", script).Run(); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}
