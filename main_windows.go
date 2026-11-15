//go:build windows

package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/oauth2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed ui.html
var uiHTML string

const appTitle = "DriveNotes"

var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	pRegisterHotKey        = user32.NewProc("RegisterHotKey")
	pGetMessageW           = user32.NewProc("GetMessageW")
	pShowWindow            = user32.NewProc("ShowWindow")
	pSetForegroundWindow   = user32.NewProc("SetForegroundWindow")
	pGetForegroundWindow   = user32.NewProc("GetForegroundWindow")
	pIsWindowVisible       = user32.NewProc("IsWindowVisible")
	pIsIconic              = user32.NewProc("IsIconic")
	pSetWindowPos          = user32.NewProc("SetWindowPos")
	pGetWindowRect         = user32.NewProc("GetWindowRect")
	pFindWindowW           = user32.NewProc("FindWindowW")
	pSetWindowLongPtrW     = user32.NewProc("SetWindowLongPtrW")
	pCallWindowProcW       = user32.NewProc("CallWindowProcW")
	pGetDpiForSystem       = user32.NewProc("GetDpiForSystem")
	pMessageBoxW           = user32.NewProc("MessageBoxW")
	pDwmSetWindowAttribute = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

const (
	swHide, swShow, swRestore = 0, 5, 9
	swMinimize, swMinNoActive = 6, 7
	wmClose, wmHotkey         = 0x0010, 0x0312
	modAlt, modCtrl, modNoRep = 0x1, 0x2, 0x4000
	swpNoSize, swpNoMove      = 0x1, 0x2
	swpNoZOrder, swpNoAct     = 0x4, 0x10
)

type rect struct{ L, T, R, B int32 }

type App struct {
	w              webview2.WebView
	hwnd           uintptr
	oldProc        uintptr
	dir            string
	cfg            *Config
	store          *Store
	sync           *Syncer
	quitting       bool
	taskbarCreated uintptr
	owner          uintptr // hidden owner window: owned windows get no taskbar button
	hotkeyOK       bool
	status         string
}

func main() {
	runtime.LockOSThread()
	hidden := len(os.Args) > 1 && os.Args[1] == "--hidden"

	// Single instance: a second launch just brings the first window forward.
	mx, _ := windows.UTF16PtrFromString(`Local\DriveNotes.SingleInstance`)
	if _, err := windows.CreateMutex(nil, false, mx); err == windows.ERROR_ALREADY_EXISTS {
		cls, _ := windows.UTF16PtrFromString("webview")
		title, _ := windows.UTF16PtrFromString(appTitle)
		find := func() uintptr {
			h, _, _ := pFindWindowW.Call(uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(title)))
			return h
		}
		if h := find(); h != 0 {
			// Ask the running copy whether it is older; if so it saves and quits
			// so this newer version takes over.
			var res uintptr
			r, _, _ := pSendMessageTimeoutW.Call(h, wmVersion, appVersion, 0, 0x2 /*SMTO_ABORTIFHUNG*/, 3000, uintptr(unsafe.Pointer(&res)))
			if r == 0 || res != 1 {
				pShowWindow.Call(h, swShow)
				pShowWindow.Call(h, swRestore)
				pSetForegroundWindow.Call(h)
				return
			}
			for i := 0; i < 60 && find() != 0; i++ { // wait for it to exit
				time.Sleep(200 * time.Millisecond)
			}
		}
	}

	a := &App{dir: filepath.Join(os.Getenv("APPDATA"), "DriveNotes"), status: "local"}
	_, statErr := os.Stat(filepath.Join(a.dir, "config.json"))
	a.cfg = LoadConfig(filepath.Join(a.dir, "config.json"))
	a.store = OpenStore(filepath.Join(a.dir, "notes.json"))
	if os.IsNotExist(statErr) && len(a.store.List()) == 0 {
		n := a.store.New()
		a.store.SetBody(n.ID, welcome)
		a.cfg.Update(func(c *Config) {})
	}
	a.sync = NewSyncer(a.store)
	a.store.OnSet = a.sync.Kick

	dpi, _, _ := pGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(filepath.Join(filepath.Dir(exe), "WebView2Loader.dll")); err != nil {
			msgBox("WebView2Loader.dll is missing.\n\nKeep DriveNotes.exe and WebView2Loader.dll together in the same folder " +
				"(unzip both from DriveNotes.zip), then start DriveNotes again.")
			return
		}
	}
	a.w = webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  filepath.Join(a.dir, "webview"),
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title: appTitle, IconId: 1, Center: true,
			Width: uint(860 * dpi / 96), Height: uint(580 * dpi / 96),
		},
	})
	if a.w == nil {
		msgBox("DriveNotes needs the Microsoft Edge WebView2 Runtime.\n\nIt will open in your browser now - install it, then start DriveNotes again.")
		openURL("https://go.microsoft.com/fwlink/p/?LinkId=2124703")
		return
	}
	a.hwnd = uintptr(a.w.Window())
	if hidden {
		if a.cfg.Taskbar {
			pShowWindow.Call(a.hwnd, swMinNoActive)
		} else {
			pShowWindow.Call(a.hwnd, swHide)
		}
	}
	a.w.SetSize(int(300*dpi/96), int(220*dpi/96), webview2.HintMin)
	if r := a.cfg.Rect; r[2] > 200 && r[3] > 150 {
		pSetWindowPos.Call(a.hwnd, 0, uintptr(r[0]), uintptr(r[1]), uintptr(r[2]), uintptr(r[3]), swpNoZOrder|swpNoAct)
	}
	a.applyTheme()
	a.setOnTop(a.cfg.OnTop)
	a.applyTaskbar(a.cfg.Taskbar)
	gwlpWndProc := ^uintptr(3) // -4
	a.oldProc, _, _ = pSetWindowLongPtrW.Call(a.hwnd, gwlpWndProc, windows.NewCallback(a.wndProc))

	a.taskbarCreated, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(utf16("TaskbarCreated"))))
	a.addTray()
	a.w.Init(navGuard)
	a.bind()
	a.w.SetHtml(uiHTML)

	a.sync.OnStatus = func(s string) {
		a.w.Dispatch(func() { a.status = s; a.eval("onStatus", s) })
	}
	a.sync.OnChange = func() { a.w.Dispatch(func() { a.eval("onNotesChanged") }) }
	if a.cfg.Token != nil {
		a.connectDrive()
	}
	go a.sync.Loop()
	go a.hotkeyLoop()

	a.w.Run()
}

const welcome = `<h1>Welcome to DriveNotes</h1>` +
	`<p>☐ Press <b>Ctrl+Alt+N</b> anywhere to show or hide this window</p>` +
	`<p>☐ Click the pin (top right) to keep the window on top of everything</p>` +
	`<p>☐ Open <b>Settings</b> (gear, bottom left) to connect Google Drive</p>` +
	`<p>☐ Click <b>Aa</b> for formatting: titles, <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strike</s>, lists</p>` +
	`<p>☐ Ctrl+L makes a checklist line. Click a box (or Ctrl+Enter) to tick it</p>` +
	`<p>☐ Notes sync as Google Docs, so you can edit them in the Google Docs app on your phone</p>`

func (a *App) eval(fn string, args ...any) {
	b, _ := json.Marshal(args)
	a.w.Eval("window." + fn + " && window." + fn + ".apply(null," + string(b) + ")")
}

func (a *App) wndProc(hwnd, msg, wp, lp uintptr) uintptr {
	if msg == 0x0112 && wp&0xFFF0 == 0xF020 && !a.cfg.Taskbar { // WM_SYSCOMMAND/SC_MINIMIZE
		a.hide()
		return 0
	}
	if msg == wmVersion { // another copy started: hand over if it is newer
		if wp > appVersion {
			a.eval("quitApp")
			return 1
		}
		return 0
	}
	if msg == wmTray {
		switch uint32(lp) & 0xFFFF {
		case 0x0202: // WM_LBUTTONUP
			a.toggle()
		case 0x0205: // WM_RBUTTONUP
			a.trayMenu()
		}
		return 0
	}
	if a.taskbarCreated != 0 && msg == a.taskbarCreated { // Explorer restarted
		a.addTray()
	}
	if msg == wmClose && !a.quitting {
		a.hide()
		return 0
	}
	r, _, _ := pCallWindowProcW.Call(a.oldProc, hwnd, msg, wp, lp)
	return r
}

func (a *App) saveRect() {
	var r rect
	if v, _, _ := pIsIconic.Call(a.hwnd); v != 0 {
		return
	}
	if ok, _, _ := pGetWindowRect.Call(a.hwnd, uintptr(unsafe.Pointer(&r))); ok != 0 {
		a.cfg.Update(func(c *Config) { c.Rect = [4]int32{r.L, r.T, r.R - r.L, r.B - r.T} })
	}
}

func (a *App) hide() {
	a.eval("flushSave")
	a.saveRect()
	if a.cfg.Taskbar {
		pShowWindow.Call(a.hwnd, swMinimize) // stays on the taskbar
	} else {
		pShowWindow.Call(a.hwnd, swHide) // tray icon only
	}
	a.sync.Kick()
}

func (a *App) show() {
	pShowWindow.Call(a.hwnd, swShow)
	if v, _, _ := pIsIconic.Call(a.hwnd); v != 0 {
		pShowWindow.Call(a.hwnd, swRestore)
	}
	pSetForegroundWindow.Call(a.hwnd)
	a.eval("focusEditor")
	a.sync.Kick() // pull anything edited on the phone
}

func (a *App) toggle() {
	vis, _, _ := pIsWindowVisible.Call(a.hwnd)
	min, _, _ := pIsIconic.Call(a.hwnd)
	fg, _, _ := pGetForegroundWindow.Call()
	// Pinned + visible counts as "showing" even when another app has focus,
	// so one press hides it instead of first just moving focus to it.
	if vis != 0 && min == 0 && (fg == a.hwnd || a.cfg.OnTop) {
		a.hide()
	} else {
		a.show()
	}
}

func (a *App) hotkeyLoop() {
	runtime.LockOSThread()
	r, _, _ := pRegisterHotKey.Call(0, 1, modCtrl|modAlt|modNoRep, 'N')
	a.hotkeyOK = r != 0
	if !a.hotkeyOK {
		return
	}
	var m struct {
		hwnd    uintptr
		message uint32
		wp, lp  uintptr
		time    uint32
		pt      [2]int32
		priv    uint32
	}
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if m.message == wmHotkey {
			a.w.Dispatch(a.toggle)
		}
	}
}

func (a *App) setOnTop(on bool) {
	after := ^uintptr(1) // HWND_NOTOPMOST (-2)
	if on {
		after = ^uintptr(0) // HWND_TOPMOST (-1)
	}
	pSetWindowPos.Call(a.hwnd, after, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoAct)
}

func (a *App) applyTheme() {
	dark := false
	if k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE); err == nil {
		v, _, err := k.GetIntegerValue("AppsUseLightTheme")
		dark = err == nil && v == 0
		k.Close()
	}
	var on uint32
	caption := uint32(0x00F2F4F6) // matches toolbar (#f6f4f2) - COLORREF is BGR
	if dark {
		on, caption = 1, 0x001F1F1F
	}
	pDwmSetWindowAttribute.Call(a.hwnd, 20, uintptr(unsafe.Pointer(&on)), 4)      // dark title bar
	pDwmSetWindowAttribute.Call(a.hwnd, 35, uintptr(unsafe.Pointer(&caption)), 4) // Win11 caption color
}

// ---------- startup ----------

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func startupCmd() string {
	exe, _ := os.Executable()
	return `"` + exe + `" --hidden`
}

func startupEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetStringValue(appTitle)
	return err == nil && strings.EqualFold(v, startupCmd())
}

func setStartup(on bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if on {
		return k.SetStringValue(appTitle, startupCmd())
	}
	_ = k.DeleteValue(appTitle)
	return nil
}

// ---------- Drive ----------

func (a *App) connectDrive() {
	c := a.cfg
	d := NewDrive(c.ClientID, c.ClientSecret, c.Token, func(t *oauth2.Token) {
		a.cfg.Update(func(c *Config) { c.Token = t })
	})
	a.sync.SetRemote(d)
	if c.Email == "" {
		go func() {
			if e := d.Email(); e != "" {
				a.cfg.Update(func(c *Config) { c.Email = e })
				a.w.Dispatch(func() { a.eval("onConnected", e) })
			}
		}()
	}
}

// ---------- JS bindings ----------

type uiState struct {
	Notes       []UINote `json:"notes"`
	OnTop       bool     `json:"onTop"`
	Connected   bool     `json:"connected"`
	Email       string   `json:"email"`
	ClientID    string   `json:"clientId"`
	HotkeyOK    bool     `json:"hotkeyOK"`
	Startup     bool     `json:"startup"`
	Status      string   `json:"status"`
	LastNote    string   `json:"lastNote"`
	SidebarOpen bool     `json:"sidebarOpen"`
	FolderURL   string   `json:"folderUrl"`
	Taskbar     bool     `json:"taskbar"`
}

func (a *App) bind() {
	w := a.w
	w.Bind("api_init", func() uiState {
		sb := a.cfg.SidebarOpen == nil || *a.cfg.SidebarOpen
		var folder string
		a.store.View(func(st *State) { folder = st.FolderID })
		fu := ""
		if folder != "" {
			fu = "https://drive.google.com/drive/folders/" + folder
		}
		time.AfterFunc(1500*time.Millisecond, func() { // hotkey thread reports late
			w.Dispatch(func() { a.eval("onHotkey", a.hotkeyOK) })
		})
		return uiState{a.store.List(), a.cfg.OnTop, a.cfg.Token != nil, a.cfg.Email, a.cfg.ClientID,
			a.hotkeyOK, startupEnabled(), a.status, a.cfg.LastNote, sb, fu, a.cfg.Taskbar}
	})
	w.Bind("api_list", a.store.List)
	w.Bind("api_new", a.store.New)
	w.Bind("api_save", a.store.SetBody)
	w.Bind("api_pin", a.store.SetPinned)
	w.Bind("api_delete", a.store.Delete)
	w.Bind("api_prefs", func(last string, sidebar bool) {
		a.cfg.Update(func(c *Config) { c.LastNote, c.SidebarOpen = last, &sidebar })
	})
	w.Bind("api_setOnTop", func(on bool) {
		a.setOnTop(on)
		a.cfg.Update(func(c *Config) { c.OnTop = on })
	})
	w.Bind("api_setTaskbar", func(on bool) {
		a.cfg.Update(func(c *Config) { c.Taskbar = on })
		a.applyTaskbar(on)
	})
	w.Bind("api_setStartup", func(on bool) (bool, error) {
		err := setStartup(on)
		return startupEnabled(), err
	})
	w.Bind("api_syncNow", func() { go a.sync.SyncNow() })
	w.Bind("api_folderUrl", func() string {
		var f string
		a.store.View(func(st *State) { f = st.FolderID })
		if f == "" {
			return ""
		}
		return "https://drive.google.com/drive/folders/" + f
	})
	w.Bind("api_open", func(u string) {
		if strings.HasPrefix(u, "https://") {
			openURL(u)
		}
	})
	w.Bind("api_openDoc", func(driveID string) {
		if driveID != "" {
			openURL(docURL(driveID))
		}
	})
	w.Bind("api_connect", func(id, secret string) {
		id, secret = strings.TrimSpace(id), strings.TrimSpace(secret)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()
			tok, err := Login(ctx, id, secret, openURL)
			w.Dispatch(func() {
				if err != nil {
					a.eval("onConnectError", err.Error())
					return
				}
				a.cfg.Update(func(c *Config) { c.ClientID, c.ClientSecret, c.Token, c.Email = id, secret, tok, "" })
				a.connectDrive()
				a.eval("onConnected", "")
				a.show()
			})
		}()
	})
	w.Bind("api_disconnect", func() {
		a.sync.SetRemote(nil)
		a.cfg.Update(func(c *Config) { c.Token, c.Email = nil, "" })
		a.status = "local"
		a.eval("onStatus", "local")
	})
	w.Bind("api_hide", a.hide)
	w.Bind("api_quit", func() {
		a.eval("flushSave")
		a.saveRect()
		pShowWindow.Call(a.hwnd, swHide)
		done := make(chan struct{})
		go func() { a.sync.SyncNow(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
		a.quitting = true
		a.removeTray()
		a.w.Destroy()
	})
}

func openURL(u string) {
	verb, _ := windows.UTF16PtrFromString("open")
	file, _ := windows.UTF16PtrFromString(u)
	_ = windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

func msgBox(text string) {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(appTitle)
	pMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), 0x40)
}

// ---------- system tray ----------

var (
	pShellNotifyIconW       = windows.NewLazySystemDLL("shell32.dll").NewProc("Shell_NotifyIconW")
	pRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	pLoadImageW             = user32.NewProc("LoadImageW")
	pGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	pCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	pAppendMenuW            = user32.NewProc("AppendMenuW")
	pTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	pDestroyMenu            = user32.NewProc("DestroyMenu")
	pGetCursorPos           = user32.NewProc("GetCursorPos")
	pPostMessageW           = user32.NewProc("PostMessageW")
)

const wmTray = 0x8000 + 1    // WM_APP+1
const wmVersion = 0x8000 + 2 // WM_APP+2

// appVersion is compared between running copies (1.5 -> 150).
const appVersion = 163

var pSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

func utf16(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }

func (a *App) nid() *notifyIconData {
	n := &notifyIconData{HWnd: a.hwnd, UID: 1}
	n.CbSize = uint32(unsafe.Sizeof(*n))
	return n
}

func (a *App) addTray() {
	var hinst windows.Handle
	_ = windows.GetModuleHandleEx(0, nil, &hinst)
	cx, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	cy, _, _ := pGetSystemMetrics.Call(50)
	icon, _, _ := pLoadImageW.Call(uintptr(hinst), 1, 1, cx, cy, 0x8000) // IMAGE_ICON, LR_SHARED
	n := a.nid()
	n.UFlags = 0x1 | 0x2 | 0x4 // NIF_MESSAGE | NIF_ICON | NIF_TIP
	n.UCallbackMessage = wmTray
	n.HIcon = icon
	copy(n.SzTip[:], windows.StringToUTF16("DriveNotes (Ctrl+Alt+N)"))
	pShellNotifyIconW.Call(0, uintptr(unsafe.Pointer(n))) // NIM_ADD
}

func (a *App) removeTray() {
	pShellNotifyIconW.Call(2, uintptr(unsafe.Pointer(a.nid()))) // NIM_DELETE
}

func (a *App) trayMenu() {
	m, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(m)
	var top uintptr
	if a.cfg.OnTop {
		top = 0x8 // MF_CHECKED
	}
	pAppendMenuW.Call(m, 0, 1, uintptr(unsafe.Pointer(utf16("Show DriveNotes\tCtrl+Alt+N"))))
	pAppendMenuW.Call(m, top, 2, uintptr(unsafe.Pointer(utf16("Keep window on top"))))
	pAppendMenuW.Call(m, 0x800, 0, 0) // separator
	pAppendMenuW.Call(m, 0, 3, uintptr(unsafe.Pointer(utf16("Quit DriveNotes"))))
	var pt [2]int32
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(a.hwnd)                                                             // so the menu closes when clicking elsewhere
	cmd, _, _ := pTrackPopupMenu.Call(m, 0x100|0x2, uintptr(pt[0]), uintptr(pt[1]), 0, a.hwnd, 0) // TPM_RETURNCMD|TPM_RIGHTBUTTON
	pPostMessageW.Call(a.hwnd, 0, 0, 0)
	switch cmd {
	case 1:
		a.show()
	case 2:
		on := !a.cfg.OnTop
		a.setOnTop(on)
		a.cfg.Update(func(c *Config) { c.OnTop = on })
		a.eval("onOnTop", on)
	case 3:
		a.eval("quitApp")
	}
}

// ---------- hardening ----------

// navGuard runs before any page script. If the view ever ends up on anything
// other than the app's own page, cut its bridge to the app and blank it.
const navGuard = `(function(){
  if (/^(about:blank|data:)/.test(location.href)) return;
  try { Object.defineProperty(window.chrome, 'webview', { value: undefined }); } catch (e) {}
  try { window.chrome.webview.postMessage = function(){}; } catch (e) {}
  document.addEventListener('DOMContentLoaded', function(){ document.documentElement.innerHTML = ''; });
  location.replace('about:blank');
})();`

// ---------- taskbar button ----------

var pCreateWindowExW = user32.NewProc("CreateWindowExW")

// applyTaskbar shows or removes the window's taskbar button. A window owned by
// a hidden window has no taskbar button but keeps its normal title bar.
func (a *App) applyTaskbar(on bool) {
	if a.owner == 0 {
		var hinst windows.Handle
		_ = windows.GetModuleHandleEx(0, nil, &hinst)
		a.owner, _, _ = pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(utf16("STATIC"))), 0, 0, 0, 0, 0, 0, 0, 0, uintptr(hinst), 0)
	}
	if m, _, _ := pIsIconic.Call(a.hwnd); m != 0 && !on {
		pShowWindow.Call(a.hwnd, swRestore)
	}
	vis, _, _ := pIsWindowVisible.Call(a.hwnd)
	if vis != 0 {
		pShowWindow.Call(a.hwnd, swHide) // the taskbar only re-checks on show
	}
	owner := a.owner
	if on {
		owner = 0
	}
	gwlpHwndParent := ^uintptr(7) // -8
	pSetWindowLongPtrW.Call(a.hwnd, gwlpHwndParent, owner)
	if vis != 0 {
		pShowWindow.Call(a.hwnd, swShow)
		a.setOnTop(a.cfg.OnTop)
	}
}
