package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const folderName = "DriveNotes"

// ---------- OAuth ----------

func oauthConfig(clientID, secret, redirect string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: secret,
		Endpoint:     google.Endpoint,
		RedirectURL:  redirect,
		// Full Drive scope so notes you create on your phone in the folder show up too.
		Scopes: []string{"https://www.googleapis.com/auth/drive"},
	}
}

// Login runs the desktop loopback OAuth flow, opening the browser with openURL.
func Login(ctx context.Context, clientID, secret string, openURL func(string)) (*oauth2.Token, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port)
	cfg := oauthConfig(clientID, secret, redirect)
	verifier := oauth2.GenerateVerifier()
	state := newID()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "bad state", 400)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if e := q.Get("error"); e != "" {
			fmt.Fprint(w, page("Sign-in cancelled", "You can close this tab."))
			select {
			case errCh <- fmt.Errorf("sign-in failed: %s", e):
			default:
			}
			return
		}
		fmt.Fprint(w, page("DriveNotes is connected ✓", "You can close this tab and go back to DriveNotes."))
		select {
		case codeCh <- q.Get("code"):
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	openURL(cfg.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce, oauth2.S256ChallengeOption(verifier)))

	select {
	case code := <-codeCh:
		return cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	case err := <-errCh:
		return nil, err
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("sign-in timed out")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func page(title, msg string) string {
	return `<!doctype html><meta charset="utf-8"><title>DriveNotes</title><body style="font-family:Segoe UI,system-ui;display:grid;place-items:center;height:90vh;background:#faf8f3;color:#222"><div style="text-align:center"><h2>` + title + `</h2><p>` + msg + `</p></div>`
}

// savingSource persists refreshed tokens.
type savingSource struct {
	mu   sync.Mutex
	src  oauth2.TokenSource
	last string
	save func(*oauth2.Token)
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.src.Token()
	if err == nil && t.AccessToken != s.last {
		s.last = t.AccessToken
		s.save(t)
	}
	return t, err
}

// ---------- Drive REST client ----------

type Drive struct {
	hc *http.Client
}

func NewDrive(clientID, secret string, tok *oauth2.Token, save func(*oauth2.Token)) *Drive {
	cfg := oauthConfig(clientID, secret, "")
	ctx := context.Background()
	src := &savingSource{src: cfg.TokenSource(ctx, tok), last: tok.AccessToken, save: save}
	hc := oauth2.NewClient(ctx, oauth2.ReuseTokenSource(tok, src))
	hc.Timeout = 60 * time.Second
	return &Drive{hc: hc}
}

const api = "https://www.googleapis.com/drive/v3/files"
const uploadAPI = "https://www.googleapis.com/upload/drive/v3/files"
const fileFields = "id,name,version,appProperties"

func (d *Drive) do(req *http.Request, out any) error {
	resp, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == 404 {
		return ErrNotFound
	}
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct{ Message string } `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		if e.Error.Message == "" {
			e.Error.Message = resp.Status
		}
		return fmt.Errorf("drive: %s", e.Error.Message)
	}
	if out != nil {
		if s, ok := out.(*string); ok {
			*s = string(b)
			return nil
		}
		return json.Unmarshal(b, out)
	}
	return nil
}

func (d *Drive) get(u string, out any) error {
	req, _ := http.NewRequest("GET", u, nil)
	return d.do(req, out)
}

func (d *Drive) sendJSON(method, u string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, u, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	return d.do(req, out)
}

type apiFile struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Trashed       bool              `json:"trashed"`
	AppProperties map[string]string `json:"appProperties"`
}

func (f apiFile) remote() RemoteFile {
	return RemoteFile{ID: f.ID, Name: f.Name, Version: f.Version, Pinned: f.AppProperties["pinned"] == "1"}
}

func (d *Drive) Email() string {
	var a struct {
		User struct{ EmailAddress string } `json:"user"`
	}
	_ = d.get("https://www.googleapis.com/drive/v3/about?fields=user(emailAddress)", &a)
	return a.User.EmailAddress
}

func (d *Drive) EnsureFolder(known string) (string, bool, error) {
	if known != "" {
		var f apiFile
		err := d.get(api+"/"+known+"?fields=id,trashed", &f)
		if err == nil && !f.Trashed {
			return known, false, nil
		}
		if err != nil && err != ErrNotFound {
			return "", false, err
		}
	}
	q := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and 'root' in parents and trashed=false", folderName)
	var l struct{ Files []apiFile }
	if err := d.get(api+"?fields=files(id)&q="+url.QueryEscape(q), &l); err != nil {
		return "", false, err
	}
	if len(l.Files) > 0 {
		return l.Files[0].ID, known != "", nil
	}
	var f apiFile
	err := d.sendJSON("POST", api+"?fields=id", map[string]any{
		"name": folderName, "mimeType": "application/vnd.google-apps.folder",
	}, &f)
	return f.ID, known != "", err
}

func (d *Drive) List(folder string) ([]RemoteFile, error) {
	q := fmt.Sprintf("'%s' in parents and trashed=false and mimeType='application/vnd.google-apps.document'", folder)
	var out []RemoteFile
	token := ""
	for {
		u := api + "?pageSize=1000&fields=nextPageToken,files(" + fileFields + ")&q=" + url.QueryEscape(q)
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		var l struct {
			NextPageToken string
			Files         []apiFile
		}
		if err := d.get(u, &l); err != nil {
			return nil, err
		}
		for _, f := range l.Files {
			out = append(out, f.remote())
		}
		if l.NextPageToken == "" {
			return out, nil
		}
		token = l.NextPageToken
	}
}

func (d *Drive) Export(id string) (string, error) {
	var s string
	err := d.get(api+"/"+id+"/export?mimeType=text/html", &s)
	return sanitizeHTML(s), err
}

func (d *Drive) upload(method, u string, meta map[string]any, body string) (RemoteFile, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/json; charset=UTF-8")
	p, _ := mw.CreatePart(h)
	_ = json.NewEncoder(p).Encode(meta)
	h = textproto.MIMEHeader{}
	h.Set("Content-Type", "text/html; charset=UTF-8")
	p, _ = mw.CreatePart(h)
	_, _ = io.WriteString(p, `<html><head><meta charset="utf-8"></head><body>`+body+`</body></html>`)
	mw.Close()
	req, _ := http.NewRequest(method, u, &buf)
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	var f apiFile
	err := d.do(req, &f)
	return f.remote(), err
}

func pinProp(p bool) map[string]string {
	if p {
		return map[string]string{"pinned": "1"}
	}
	return map[string]string{"pinned": "0"}
}

func (d *Drive) Create(folder, name, body string, pinned bool) (RemoteFile, error) {
	return d.upload("POST", uploadAPI+"?uploadType=multipart&fields="+fileFields, map[string]any{
		"name": name, "mimeType": "application/vnd.google-apps.document",
		"parents": []string{folder}, "appProperties": pinProp(pinned),
	}, body)
}

func (d *Drive) Update(id, name, body string, pinned bool) (RemoteFile, error) {
	return d.upload("PATCH", uploadAPI+"/"+id+"?uploadType=multipart&fields="+fileFields, map[string]any{
		"name": name, "appProperties": pinProp(pinned),
	}, body)
}

func (d *Drive) Trash(id string) error {
	return d.sendJSON("PATCH", api+"/"+id, map[string]any{"trashed": true}, nil)
}

func docURL(id string) string { return "https://docs.google.com/document/d/" + id + "/edit" }
