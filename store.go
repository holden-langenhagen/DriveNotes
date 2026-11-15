package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Note is one note. A note is "dirty" (needs upload) whenever its Body/Pinned
// differ from what was last synced, or it has never been uploaded.
type Note struct {
	ID            string `json:"id"`
	Body          string `json:"body"`
	Pinned        bool   `json:"pinned"`
	Updated       int64  `json:"updated"` // unix ms
	DriveID       string `json:"driveId,omitempty"`
	RemoteVersion string `json:"remoteVersion,omitempty"`
	SyncedPinned  bool   `json:"syncedPinned,omitempty"`
	Fmt           string `json:"fmt,omitempty"`        // "html" (old notes were plain text)
	BaseLocal     string `json:"baseLocal,omitempty"`  // Body as of last sync
	BaseRemote    string `json:"baseRemote,omitempty"` // Docs export as of last sync
}

func (n *Note) dirty() bool {
	return n.DriveID == "" || n.localEdited() || n.Pinned != n.SyncedPinned
}

func (n *Note) localEdited() bool { return canon(n.Body) != canon(n.BaseLocal) }

type State struct {
	Notes    []*Note  `json:"notes"`
	Deleted  []string `json:"deleted"` // Drive IDs waiting to be trashed
	FolderID string   `json:"folderId"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	st    State
	OnSet func() // called after any mutation (used to kick sync)
}

func OpenStore(path string) *Store {
	s := &Store{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.st)
	}
	for _, n := range s.st.Notes { // migrate plain-text notes to HTML
		if n.Fmt != "html" {
			n.Body = textToHTML(n.Body)
			n.BaseLocal, n.BaseRemote, n.RemoteVersion, n.Fmt = n.Body, "", "", "html"
		}
	}
	return s
}

func (s *Store) saveLocked() {
	b, _ := json.MarshalIndent(&s.st, "", " ")
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// Mutate runs f under the lock and persists the result.
func (s *Store) Mutate(f func(st *State)) {
	s.mu.Lock()
	f(&s.st)
	s.saveLocked()
	s.mu.Unlock()
}

// userMutate is Mutate plus a sync kick (for edits made by the user).
func (s *Store) userMutate(f func(st *State)) {
	s.Mutate(f)
	if s.OnSet != nil {
		s.OnSet()
	}
}

// View runs f under the lock without saving.
func (s *Store) View(f func(st *State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.st)
}

func findNote(st *State, id string) *Note {
	for _, n := range st.Notes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

func findByDrive(st *State, driveID string) *Note {
	for _, n := range st.Notes {
		if n.DriveID == driveID {
			return n
		}
	}
	return nil
}

// UINote is what the front end sees.
type UINote struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	Pinned  bool   `json:"pinned"`
	Updated int64  `json:"updated"`
	DriveID string `json:"driveId"`
}

func (s *Store) List() []UINote {
	out := []UINote{}
	s.View(func(st *State) {
		for _, n := range st.Notes {
			out = append(out, UINote{n.ID, n.Body, n.Pinned, n.Updated, n.DriveID})
		}
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Updated > out[j].Updated
	})
	return out
}

func (s *Store) New() UINote {
	n := &Note{ID: newID(), Updated: nowMs(), Fmt: "html"}
	s.mu.Lock()
	s.st.Notes = append(s.st.Notes, n)
	s.saveLocked()
	s.mu.Unlock()
	return UINote{ID: n.ID, Updated: n.Updated}
}

func (s *Store) SetBody(id, body string) {
	s.userMutate(func(st *State) {
		if n := findNote(st, id); n != nil && n.Body != body {
			n.Body = body
			n.Updated = nowMs()
		}
	})
}

func (s *Store) SetPinned(id string, pinned bool) {
	s.userMutate(func(st *State) {
		if n := findNote(st, id); n != nil {
			n.Pinned = pinned
			n.Updated = nowMs()
		}
	})
}

func (s *Store) Delete(id string) {
	s.userMutate(func(st *State) {
		for i, n := range st.Notes {
			if n.ID == id {
				if n.DriveID != "" {
					st.Deleted = append(st.Deleted, n.DriveID)
				}
				st.Notes = append(st.Notes[:i], st.Notes[i+1:]...)
				return
			}
		}
	})
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nowMs() int64 { return time.Now().UnixMilli() }

// norm makes text comparable across the Google Docs round trip
// (BOM, CRLF, trailing whitespace / non-breaking spaces).
func norm(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.ReplaceAll(s, " ", " ")
	s = strings.ReplaceAll(s, "\v", "\n") // Docs soft line breaks
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// titleOf returns the Drive document name for a note body.
func titleOf(body string) string {
	for _, l := range strings.Split(htmlToText(body), "\n") {
		l = strings.TrimSpace(strings.TrimLeft(l, "#☐☑•-* \t"))
		if l != "" {
			r := []rune(l)
			if len(r) > 80 {
				r = append(r[:80], '…')
			}
			return string(r)
		}
	}
	return "Untitled note"
}
