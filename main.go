package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/software"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/fsnotify/fsnotify"
)

//go:embed resources/icon.svg
var iconData []byte

const (
	appID         = "ch.rdpfui"
	autostartID   = "ch.rdpfui.autostart"
	configAppName = "rdpfui"
)

type WatchFolder struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	Regex     string `json:"regex,omitempty"`
}

type Config struct {
	Folders   []WatchFolder `json:"folders"`
	Autostart bool          `json:"autostart"`
}

var (
	appInstance  fyne.App
	config       Config
	watcher      *fsnotify.Watcher
	watchedDirs  map[string]bool
	watchLock    sync.Mutex
	events       chan string
	statusLabel  *widget.Label
	logView      *widget.Entry
	logMutex     sync.Mutex
	logPending   []string
	folderList   *widget.List
	recursiveCB  *widget.Check
	regexEntry   *widget.Entry
	saveRegexBtn *widget.Button
	autostartCB  *widget.Check
	removeBtn    *widget.Button
	selectedIdx  int
)

func main() {
	appInstance = app.NewWithID(appID)
	icon := fyne.NewStaticResource("icon.svg", iconData)
	appInstance.SetIcon(icon)

	hasTrayIcon := false
	hasTrayMenu := false
	hasTrayWindow := false
	var trayWindowSetter interface {
		SetSystemTrayWindow(fyne.Window)
	}
	if desktopApp, ok := appInstance.(interface {
		SetSystemTrayIcon(fyne.Resource)
	}); ok {
		desktopApp.SetSystemTrayIcon(icon)
		hasTrayIcon = true
	}
	if _, ok := appInstance.(interface {
		SetSystemTrayMenu(*fyne.Menu)
	}); ok {
		hasTrayMenu = true
	}
	if desktopApp, ok := appInstance.(interface {
		SetSystemTrayWindow(fyne.Window)
	}); ok {
		hasTrayWindow = true
		trayWindowSetter = desktopApp
	}

	config = loadConfig()
	events = make(chan string, 32)
	watchedDirs = make(map[string]bool)

	window := appInstance.NewWindow("rdpfui")
	window.SetIcon(icon)
	window.SetCloseIntercept(func() {
		if hasTrayIcon || hasTrayMenu || hasTrayWindow {
			window.Hide()
			return
		}
		appInstance.Quit()
	})

	folderList = widget.NewList(
		func() int { return len(config.Folders) },
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(i int, o fyne.CanvasObject) {
			label := o.(*widget.Label)
			folder := config.Folders[i]
			suffix := ""
			if folder.Recursive {
				suffix = " (recursive)"
			}
			if folder.Regex != "" {
				if suffix != "" {
					suffix += " "
				}
				suffix += "[filter]"
			}
			label.SetText(folder.Path + suffix)
		},
	)

	folderList.OnSelected = func(id int) {
		selectedIdx = id
		recursiveCB.SetChecked(config.Folders[id].Recursive)
		regexEntry.SetText(config.Folders[id].Regex)
		regexEntry.Enable()
		saveRegexBtn.Enable()
		recursiveCB.Enable()
		removeBtn.Enable()
	}

	folderList.OnUnselected = func(id int) {
		selectedIdx = -1
		recursiveCB.SetChecked(false)
		regexEntry.SetText("")
		regexEntry.Disable()
		saveRegexBtn.Disable()
		removeBtn.Disable()
	}

	addBtn := widget.NewButton("Add folder", func() {
		dlg := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			path := uri.Path()
			addFolder(path)
		}, window)
		dlg.Show()
	})

	removeBtn = widget.NewButton("Remove selected", func() {
		if selectedIdx < 0 || selectedIdx >= len(config.Folders) {
			return
		}
		removeFolder(selectedIdx)
		selectedIdx = -1
		recursiveCB.SetChecked(false)
		recursiveCB.Disable()
		removeBtn.Disable()
	})
	removeBtn.Disable()

	regexEntry = widget.NewEntry()
	regexEntry.SetPlaceHolder("Optional regex to match .rdp file contents")
	regexEntry.Disable()

	saveRegexBtn = widget.NewButton("Save filter", func() {
		if selectedIdx < 0 || selectedIdx >= len(config.Folders) {
			return
		}
		pattern := strings.TrimSpace(regexEntry.Text)
		if pattern != "" {
			if _, err := regexp.Compile(pattern); err != nil {
				dialog.ShowError(err, window)
				return
			}
		}
		config.Folders[selectedIdx].Regex = pattern
		saveConfig()
		updateStatus("Updated regex filter for selected folder")
		refreshFolderList()
	})
	saveRegexBtn.Disable()

	recursiveCB = widget.NewCheck("Watch recursively", func(checked bool) {
		if selectedIdx < 0 || selectedIdx >= len(config.Folders) {
			return
		}
		config.Folders[selectedIdx].Recursive = checked
		saveConfig()
		restartWatching()
		refreshFolderList()
	})
	recursiveCB.Disable()

	autostartCB = widget.NewCheck("Enable autostart", func(checked bool) {
		config.Autostart = checked
		err := setAutostart(checked)
		if err != nil {
			updateStatus("Autostart error: " + err.Error())
		}
		saveConfig()
	})
	autostartCB.SetChecked(config.Autostart)

	statusLabel = widget.NewLabel("Ready")
	statusLabel.Wrapping = fyne.TextWrapWord

	logView = widget.NewMultiLineEntry()
	logView.Disable()
	logView.SetMinRowsVisible(6)

	title := canvas.NewText("rdpfui", theme.ForegroundColor())
	title.TextSize = 34
	title.TextStyle = fyne.TextStyle{Bold: true}
	title.Alignment = fyne.TextAlignLeading

	subtitle := widget.NewLabelWithStyle("Watch .rdp folders and reopen files automatically.", fyne.TextAlignLeading, fyne.TextStyle{})

	controlPanel := container.NewVBox(
		addBtn,
		removeBtn,
		recursiveCB,
		widget.NewLabel("Regex content filter:"),
		regexEntry,
		saveRegexBtn,
		widget.NewSeparator(),
		autostartCB,
		widget.NewSeparator(),
		statusLabel,
	)

	folderCard := widget.NewCard("Watched folders", "Add folders to monitor for .rdp files.", container.NewVScroll(folderList))
	settingsCard := widget.NewCard("Controls", "Manage selected folder and autostart settings.", controlPanel)

	mainContent := container.NewHSplit(folderCard, settingsCard)
	mainContent.SetOffset(0.55)

	window.SetContent(container.NewVBox(title, subtitle, widget.NewSeparator(), mainContent, widget.NewLabel("Log:"), logView))
	window.Resize(fyne.NewSize(760, 560))

	updateStatus("Initializing watcher...")
	logMessage("main: starting UI and watcher initialization")

	menu := fyne.NewMenu("rdpfui",
		fyne.NewMenuItem("Show", func() {
			window.Show()
			window.RequestFocus()
		}),
		fyne.NewMenuItem("Quit", func() {
			appInstance.Quit()
		}),
	)
	if hasTrayMenu {
		if desktopApp, ok := appInstance.(interface {
			SetSystemTrayMenu(*fyne.Menu)
		}); ok {
			desktopApp.SetSystemTrayMenu(menu)
		}
	}
	if hasTrayWindow {
		if setter, ok := trayWindowSetter.(interface {
			SetSystemTrayWindow(fyne.Window)
		}); ok {
			setter.SetSystemTrayWindow(window)
		}
	}

	if runtime.GOOS == "darwin" && (hasTrayIcon || hasTrayMenu || hasTrayWindow) {
		setupMacOS(icon)
	}

	if len(config.Folders) > 0 {
		folderList.Refresh()
	}

	window.Show()
	go func() {
		err := startWatching()
		if err != nil {
			updateStatus("Watcher error: " + err.Error())
			return
		}
		scanAllFolders()
		go watchLoop()
		go processEvents()
	}()
	go flushLogsLoop()

	defer stopWatching()
	window.ShowAndRun()
}

func ensureConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir = filepath.Join(dir, configAppName)
	return dir, os.MkdirAll(dir, 0o755)
}

func configPath() string {
	dir, err := ensureConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "config.json")
}

func loadConfig() Config {
	cfg := Config{Folders: []WatchFolder{}}
	path := configPath()
	if path == "" {
		return cfg
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}
	_ = json.Unmarshal(data, &cfg)
	return cfg
}

func saveConfig() {
	path := configPath()
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(config, "  ", "  ")
	if err != nil {
		updateStatus("Config save failed: " + err.Error())
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}

func addFolder(path string) {
	for _, folder := range config.Folders {
		if folder.Path == path {
			updateStatus("Folder already watched")
			return
		}
	}
	config.Folders = append(config.Folders, WatchFolder{Path: path, Recursive: false, Regex: ""})
	saveConfig()
	restartWatching()
	refreshFolderList()
	updateStatus("Added " + path)
}

func removeFolder(index int) {
	if index < 0 || index >= len(config.Folders) {
		return
	}
	path := config.Folders[index].Path
	config.Folders = append(config.Folders[:index], config.Folders[index+1:]...)
	saveConfig()
	restartWatching()
	refreshFolderList()
	updateStatus("Removed " + path)
}

func refreshFolderList() {
	runOnUI(func() {
		folderList.Refresh()
	})
}

func startWatching() error {
	watchLock.Lock()
	if watcher != nil {
		watchLock.Unlock()
		return nil
	}
	var err error
	watcher, err = fsnotify.NewWatcher()
	if err != nil {
		watchLock.Unlock()
		logMessage("startWatching: failed to create watcher: %v", err)
		return err
	}
	watchedDirs = make(map[string]bool)
	watchLock.Unlock()

	for _, folder := range config.Folders {
		addWatchFolder(folder)
	}
	logMessage("startWatching: watcher created, watching %d directories", len(watchedDirs))
	updateStatus(fmt.Sprintf("Watching %d directories", len(watchedDirs)))
	return nil
}

func stopWatching() {
	watchLock.Lock()
	defer watchLock.Unlock()
	if watcher != nil {
		_ = watcher.Close()
		watcher = nil
	}
}

func restartWatching() {
	go func() {
		stopWatching()
		if err := startWatching(); err != nil {
			updateStatus("Watcher error: " + err.Error())
			return
		}
		go watchLoop()
		go processEvents()
		logMessage("restartWatching: watcher restarted")
		updateStatus(fmt.Sprintf("Watching %d directories", len(watchedDirs)))
	}()
}

func addWatchFolder(folder WatchFolder) {
	if folder.Path == "" {
		return
	}
	if folder.Recursive {
		_ = filepath.WalkDir(folder.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			_ = addWatcher(path)
			return nil
		})
	} else {
		_ = addWatcher(folder.Path)
	}
}

func addWatcher(path string) error {
	watchLock.Lock()
	defer watchLock.Unlock()
	if watcher == nil {
		return errors.New("watcher not initialized")
	}
	if watchedDirs[path] {
		return nil
	}
	err := watcher.Add(path)
	if err != nil {
		logMessage("addWatcher: failed to add %s: %v", path, err)
		return err
	}
	watchedDirs[path] = true
	logMessage("addWatcher: watching %s", path)
	return nil
}

func watchLoop() {
	for {
		watchLock.Lock()
		w := watcher
		watchLock.Unlock()
		if w == nil {
			return
		}
		select {
		case event, ok := <-w.Events:
			if !ok {
				return
			}
			handleFsEvent(event)
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			updateStatus("Watcher error: " + err.Error())
		}
	}
}

func handleFsEvent(event fsnotify.Event) {
	if strings.EqualFold(filepath.Ext(event.Name), ".rdp") && event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename|fsnotify.Chmod) != 0 {
		logMessage("handleFsEvent: .rdp event %s %v", event.Name, event.Op)
		queueRdp(event.Name)
		return
	}

	if event.Op&fsnotify.Create != 0 {
		info, err := os.Stat(event.Name)
		if err == nil && info.IsDir() {
			for _, folder := range config.Folders {
				if folder.Recursive && strings.HasPrefix(event.Name, folder.Path) {
					_ = addWatcher(event.Name)
					break
				}
			}
		}
	}
}

func queueRdp(path string) {
	select {
	case events <- path:
	default:
		// drop if busy
	}
}

func processEvents() {
	for path := range events {
		time.Sleep(400 * time.Millisecond)
		handleRdpFile(path)
	}
}

func handleRdpFile(path string) {
	if !strings.EqualFold(filepath.Ext(path), ".rdp") {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	if !passesRdpFilter(path) {
		logMessage("handleRdpFile: skipped %s because content did not match regex filter", path)
		return
	}
	destDir := filepath.Join(os.TempDir(), configAppName)
	_ = os.MkdirAll(destDir, 0o755)
	dest := filepath.Join(destDir, filepath.Base(path))
	if err := moveFile(path, dest); err != nil {
		updateStatus("Move failed: " + err.Error())
		return
	}
	if err := openPath(dest); err != nil {
		updateStatus("Open failed: " + err.Error())
		return
	}
	updateStatus("Opened " + filepath.Base(dest))
}

func moveFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrInvalid) && !strings.Contains(err.Error(), "cross-device") {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Remove(src)
}

func openPath(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/C", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

func scanAllFolders() {
	for _, folder := range config.Folders {
		scanFolder(folder)
	}
}

func scanFolder(folder WatchFolder) {
	if folder.Path == "" {
		return
	}
	if folder.Recursive {
		_ = filepath.WalkDir(folder.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if strings.EqualFold(filepath.Ext(path), ".rdp") && passesRdpFilter(path) {
				queueRdp(path)
			}
			return nil
		})
	} else {
		entries, err := os.ReadDir(folder.Path)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if strings.EqualFold(filepath.Ext(entry.Name()), ".rdp") {
				path := filepath.Join(folder.Path, entry.Name())
				if passesRdpFilter(path) {
					queueRdp(path)
				}
			}
		}
	}
}

func passesRdpFilter(path string) bool {
	matchedRegex := false
	for _, folder := range config.Folders {
		if !folderMatchesPath(folder, path) {
			continue
		}
		if folder.Regex == "" {
			return true
		}
		matchedRegex = true
		ok, err := rdpContentMatches(folder.Regex, path)
		if err != nil {
			logMessage("passesRdpFilter: invalid regex for folder %s: %v", folder.Path, err)
			continue
		}
		if ok {
			return true
		}
	}
	if matchedRegex {
		return false
	}
	return true
}

func folderMatchesPath(folder WatchFolder, path string) bool {
	rel, err := filepath.Rel(folder.Path, path)
	if err != nil {
		return false
	}
	if folder.Recursive {
		return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
	}
	return filepath.Dir(path) == folder.Path
}

func rdpContentMatches(pattern, path string) (bool, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(data)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimRight(text, "\n")
	return re.MatchString(text), nil
}

func runOnUI(fn func()) {
	if appInstance == nil {
		fn()
		return
	}
	if driver := fyne.CurrentApp().Driver(); driver != nil {
		driver.DoFromGoroutine(fn, false)
		return
	}
	fn()
}

func updateStatus(text string) {
	runOnUI(func() {
		if statusLabel != nil {
			statusLabel.SetText(text)
		}
	})
}

func logMessage(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	log.Println(msg)
	logMutex.Lock()
	logPending = append(logPending, msg)
	logMutex.Unlock()
}

func flushLogsLoop() {
	for range time.Tick(250 * time.Millisecond) {
		logMutex.Lock()
		if len(logPending) == 0 {
			logMutex.Unlock()
			continue
		}
		pending := logPending
		logPending = nil
		logMutex.Unlock()

		updateLog(strings.Join(pending, "\n"))
	}
}

func updateLog(text string) {
	runOnUI(func() {
		if logView != nil {
			current := logView.Text
			if current != "" {
				current += "\n"
			}
			logView.SetText(current + text)
		}
	})
}

func setupMacOS(icon fyne.Resource) {
	if runtime.GOOS != "darwin" || icon == nil {
		return
	}

	iconBytes, err := macOSAppIconBytes(icon)
	if err != nil {
		logMessage("setupMacOS: failed to convert icon: %v", err)
	} else if len(iconBytes) > 0 {
		_ = setMacAppIcon(iconBytes)
	}

	hideMacDockIcon()
}

func macOSAppIconBytes(icon fyne.Resource) ([]byte, error) {
	data := icon.Content()
	if len(data) == 0 {
		return nil, nil
	}

	if strings.HasSuffix(strings.ToLower(icon.Name()), ".svg") || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<svg")) {
		img := canvas.NewImageFromResource(icon)
		c := software.NewTransparentCanvas()
		c.SetContent(img)
		c.SetPadded(false)
		c.Resize(fyne.NewSquareSize(256))

		buf := &bytes.Buffer{}
		if err := png.Encode(buf, c.Capture()); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}

	return data, nil
}

func setAutostart(enabled bool) error {
	switch runtime.GOOS {
	case "darwin":
		return setAutostartDarwin(enabled)
	case "linux":
		return setAutostartLinux(enabled)
	case "windows":
		return setAutostartWindows(enabled)
	default:
		return errors.New("autostart not supported on this platform")
	}
}

func setAutostartDarwin(enabled bool) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	agents := filepath.Join(home, "Library", "LaunchAgents")
	_ = os.MkdirAll(agents, 0o755)
	plistPath := filepath.Join(agents, autostartID+".plist")
	if !enabled {
		return os.Remove(plistPath)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
  </dict>
</plist>
`, autostartID, exe)
	return os.WriteFile(plistPath, []byte(content), 0o644)
}

func setAutostartLinux(enabled bool) error {
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	autostartDir := filepath.Join(dir, "autostart")
	_ = os.MkdirAll(autostartDir, 0o755)
	desktopPath := filepath.Join(autostartDir, "rdpfui.desktop")
	if !enabled {
		return os.Remove(desktopPath)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=rdpfui
Exec="%s"
X-GNOME-Autostart-enabled=true
`, exe)
	return os.WriteFile(desktopPath, []byte(content), 0o644)
}

func setAutostartWindows(enabled bool) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	runKey := `HKCU\\Software\\Microsoft\\Windows\\CurrentVersion\\Run`
	name := "rdpfui"
	if !enabled {
		cmd := exec.Command("reg", "delete", runKey, "/v", name, "/f")
		return cmd.Run()
	}
	cmd := exec.Command("reg", "add", runKey, "/v", name, "/d", exe, "/f")
	return cmd.Run()
}
