# DriveNotes

A tiny macOS-Notes-style notes app for Windows that syncs every note to Google Drive as a Google Doc
(folder **DriveNotes**), so you can edit notes from the Google Docs app on your phone.

## Download & run
1. Go to **[Releases](https://github.com/holden-langenhagen/DriveNotes/releases/latest)** and download `DriveNotes.zip`.
2. Unzip it and move `DriveNotes.exe` somewhere permanent (e.g. Documents). Don't run it from inside the zip.
3. Double-click it. No install needed. Windows 10/11 (64-bit).
   The app isn't code-signed, so SmartScreen may say "Windows protected your PC": click **More info → Run anyway**.
   Prefer not to trust a downloaded exe? [Build it yourself](#build-it-yourself) — it takes about two minutes.

Your notes and settings live in `%APPDATA%\DriveNotes`. Updating = quit DriveNotes (tray icon → Quit) and replace the exe.

## Shortcuts
| Keys | Action |
|---|---|
| Ctrl+Alt+N | Show / hide from anywhere |
| Ctrl+T or pin button | Keep window on top |
| Ctrl+N | New note |
| Ctrl+L | Checklist line (☐) — click box or Ctrl+Enter to tick |
| Ctrl+Shift+P / star | Pin note to top of list |
| Aa button | Formatting: Title, Heading, Body, bold, italic, underline, strikethrough, lists |
| Ctrl+B / I / U, Ctrl+Shift+X | Bold / italic / underline / strikethrough |
| `- `, `1. `, `[] ` at line start | Bulleted list / numbered list / checklist |
| Ctrl+F | Search |
| Esc / close button | Minimize to taskbar (keeps running). Quit from Settings or the tray icon |

## Connect Google Drive (one time, ~3 min)
1. https://console.cloud.google.com/projectcreate → project "DriveNotes".
2. Enable the **Google Drive API**: https://console.cloud.google.com/apis/library/drive.googleapis.com
3. **Google Auth Platform** → Get started: name DriveNotes, your email, audience **External**.
4. **Audience** → **Publish app** → Confirm. If you skip this you get "Error 403: access_denied ... can only be accessed by developer-approved testers". (Alternative: add your Gmail under Audience → Test users, but then Google signs you out every 7 days.)
5. **Clients** → Create client → **Desktop app**. Paste the Client ID + secret in DriveNotes → Settings → Connect.
6. On "Google hasn't verified this app" click **Advanced → Go to DriveNotes** (it's your own app).

## Sync behaviour
* Pushes ~2 s after you stop typing, pulls every 30 s and whenever you open the window.
* Works offline; syncs when back online.
* If the same note is edited on both phone and PC between syncs, both versions are kept.
* Deleting a note moves its Doc to Drive trash.
* Formatting (titles, headings, bold, italic, underline, strikethrough, lists) syncs both ways with Google Docs.
  Fonts, colors, images and tables added in Docs are simplified.

## Build it yourself
You only need [Go](https://go.dev/dl/) (1.26 or newer). No C compiler, no Visual Studio.

**On Windows (PowerShell):**
```powershell
git clone https://github.com/holden-langenhagen/DriveNotes.git
cd DriveNotes
go run ./tools/genres
go build -trimpath -ldflags "-s -w -H windowsgui" -o DriveNotes.exe .
```
(No git? Use **Code → Download ZIP** on GitHub, unzip, and run the last three lines in that folder.)

**On macOS / Linux** (cross-compiles a Windows exe):
```bash
go run ./tools/genres
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o DriveNotes.exe .
```

* `go run ./tools/genres` embeds the icon and the Windows manifest (crisp text on high-DPI screens). Skip it and the app still works, just with a default icon.
* `-H windowsgui` stops a console window from opening; `-trimpath -s -w` keeps the exe small and free of your local paths.
* Run the tests with `go test .`.

**Official builds** are made by GitHub Actions from this exact source ([workflow](.github/workflows/release.yml)): pushing a tag like `v1.5.2` builds the exe, runs the tests and attaches `DriveNotes.zip` to a release. You can also run it by hand from the **Actions** tab (**Build & release → Run workflow**); entering an existing version rebuilds that release from the current code.

## Privacy & security
* **No secrets in this repo or the exe.** Each user creates their own Google OAuth client; its ID/secret are entered in the app and never compiled in.
* **Credentials are encrypted at rest** with Windows DPAPI (per-user), in `%APPDATA%\DriveNotes\config.json`. A copied config file is useless on another account or PC.
* **Notes** are stored locally in `%APPDATA%\DriveNotes\notes.json` and in your own Google Drive folder `DriveNotes`. Nothing is sent anywhere else — there is no server, telemetry or analytics.
* **Network:** the app talks only to `accounts.google.com` / `oauth2.googleapis.com` (sign-in) and `www.googleapis.com` (Drive). Sign-in uses PKCE and a one-time loopback listener on `127.0.0.1` with a random `state` check.
* **UI isolation:** the window shows only the app's embedded page under a strict Content-Security-Policy (no external scripts, images, fonts or connections). Dropped links/files can't navigate it, and if it ever leaves the app page its bridge to the app is cut. Note content from Docs or the clipboard is reduced to plain formatting tags with no attributes, so it can't run scripts.
* **Scope:** the app requests full Drive access so notes you create in the `DriveNotes` folder on your phone appear on the PC. It only reads/writes inside that folder. Revoke any time at https://myaccount.google.com/permissions.
