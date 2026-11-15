# DriveNotes

A tiny macOS-Notes-style notes app for Windows that syncs every note to Google Drive as a Google Doc
(folder **DriveNotes**), so you can edit notes from the Google Docs app on your phone.

## Run
Double-click `DriveNotes.exe`. No install. (Unsigned app: if SmartScreen appears, click **More info → Run anyway**.)
Data lives in `%APPDATA%\DriveNotes`.

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

## Build (from any OS)
```
go run ./tools/genres          # icon + manifest resource
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o DriveNotes.exe .
go test .                      # sync engine tests
```

## Privacy & security
* **No secrets in this repo or the exe.** Each user creates their own Google OAuth client; its ID/secret are entered in the app and never compiled in.
* **Credentials are encrypted at rest** with Windows DPAPI (per-user), in `%APPDATA%\DriveNotes\config.json`. A copied config file is useless on another account or PC.
* **Notes** are stored locally in `%APPDATA%\DriveNotes\notes.json` and in your own Google Drive folder `DriveNotes`. Nothing is sent anywhere else — there is no server, telemetry or analytics.
* **Network:** the app talks only to `accounts.google.com` / `oauth2.googleapis.com` (sign-in) and `www.googleapis.com` (Drive). Sign-in uses PKCE and a one-time loopback listener on `127.0.0.1` with a random `state` check.
* **UI isolation:** the window shows only the app's embedded page under a strict Content-Security-Policy (no external scripts, images, fonts or connections). Dropped links/files can't navigate it, and if it ever leaves the app page its bridge to the app is cut. Note content from Docs or the clipboard is reduced to plain formatting tags with no attributes, so it can't run scripts.
* **Scope:** the app requests full Drive access so notes you create in the `DriveNotes` folder on your phone appear on the PC. It only reads/writes inside that folder. Revoke any time at https://myaccount.google.com/permissions.
