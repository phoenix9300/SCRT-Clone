package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

var errNoAppBrowser = errors.New("no Chromium-based browser found for the application window")

// appBrowsers lists Chromium-family browsers that support --app windows.
func appBrowsers() []string {
	if b := os.Getenv("HAWSER_BROWSER"); b != "" {
		return []string{b}
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
			"/Applications/Vivaldi.app/Contents/MacOS/Vivaldi",
		}
	case "windows":
		var out []string
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			base := os.Getenv(env)
			if base == "" {
				continue
			}
			out = append(out,
				filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
				filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `Chromium\Application\chrome.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`),
			)
		}
		return out
	default:
		return []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "brave-browser", "vivaldi"}
	}
}

// openAppWindow opens url in a chromeless application window with its own
// profile. The returned function blocks until that window is closed.
func openAppWindow(url, profileDir string) (wait func(), err error) {
	for _, cand := range appBrowsers() {
		path := cand
		if !filepath.IsAbs(cand) {
			p, lerr := exec.LookPath(cand)
			if lerr != nil {
				continue
			}
			path = p
		} else if _, serr := os.Stat(cand); serr != nil {
			continue
		}
		cmd := exec.Command(path,
			"--app="+url,
			"--user-data-dir="+profileDir,
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-features=Translate,MediaRouter",
			"--window-size=1360,860",
		)
		if err := cmd.Start(); err != nil {
			continue
		}
		return func() { _ = cmd.Wait() }, nil
	}
	return nil, errNoAppBrowser
}

func openDefaultBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
