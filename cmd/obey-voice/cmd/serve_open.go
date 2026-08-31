//go:build !integration

package cmd

import (
	"fmt"
	"os/exec"
	"runtime"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
)

// openURL launches url in the desktop browser. Tests replace this.
var openURL = openURLDefault

func openURLDefault(url string) error {
	if url == "" {
		return fmt.Errorf("empty url")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func openStage(listenAddr string, creds *netapi.Credentials) {
	u := stageServeURL(clientPageURL(listenAddr), creds)
	if u == "" {
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  --open: no pairing code; Stage was not opened"))
		return
	}
	if err := openURL(u); err != nil {
		fmt.Fprintf(serveHumanOut, "  warning: --open failed: %v\n", err)
		fmt.Fprintln(serveHumanOut, dimStyle.Render("  Open this URL: "+u))
		return
	}
	fmt.Fprintln(serveHumanOut, dimStyle.Render("  Opened Stage in the default browser."))
}
