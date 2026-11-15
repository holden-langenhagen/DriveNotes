package main

import (
	"errors"
	"strings"
	"sync"
	"time"
)

type RemoteFile struct {
	ID      string
	Name    string
	Version string
	Pinned  bool
}

var ErrNotFound = errors.New("not found")

// Remote is the subset of Google Drive the syncer needs.
type Remote interface {
	EnsureFolder(knownID string) (string, bool, error) // id, recreated?, err
	List(folderID string) ([]RemoteFile, error)
	Export(id string) (string, error)
	Create(folderID, name, body string, pinned bool) (RemoteFile, error)
	Update(id, name, body string, pinned bool) (RemoteFile, error)
	Trash(id string) error
}

type Syncer struct {
	store    *Store
	mu       sync.Mutex
	remote   Remote
	kick     chan struct{}
	Interval func() time.Duration // polling pace; nil = every 30s
	OnChange func()               // local notes changed by sync
	OnStatus func(status string)  // human-readable status
}

func NewSyncer(store *Store) *Syncer {
	return &Syncer{store: store, kick: make(chan struct{}, 1)}
}

func (s *Syncer) SetRemote(r Remote) {
	s.mu.Lock()
	s.remote = r
	s.mu.Unlock()
	s.Kick()
}

func (s *Syncer) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Loop syncs shortly after local edits or a Kick (e.g. the window being
// shown), and otherwise polls Drive at the pace Interval returns, which is
// re-read every cycle so it follows visibility and power state.
func (s *Syncer) Loop() {
	for {
		d := 30 * time.Second
		if s.Interval != nil {
			d = s.Interval()
		}
		t := time.NewTimer(d)
		select {
		case <-s.kick:
			t.Stop()
			time.Sleep(1500 * time.Millisecond)
			select { // collapse kicks that arrived during the debounce
			case <-s.kick:
			default:
			}
		case <-t.C:
		}
		s.SyncNow()
	}
}

func (s *Syncer) status(msg string) {
	if s.OnStatus != nil {
		s.OnStatus(msg)
	}
}

func (s *Syncer) SyncNow() {
	s.mu.Lock()
	r := s.remote
	s.mu.Unlock()
	if r == nil {
		s.status("local")
		return
	}
	s.status("syncing")
	changed, err := s.syncOnce(r)
	if changed && s.OnChange != nil {
		s.OnChange()
	}
	if err != nil {
		s.status("error:" + err.Error())
		return
	}
	s.status("synced:" + time.Now().Format("3:04 PM"))
}

// syncOnce performs one full two-way sync. Returns whether local notes changed.
func (s *Syncer) syncOnce(r Remote) (changed bool, err error) {
	s.mu.Lock() // one sync at a time
	defer s.mu.Unlock()

	var folder string
	s.store.View(func(st *State) { folder = st.FolderID })
	folder, recreated, err := r.EnsureFolder(folder)
	if err != nil {
		return false, err
	}
	s.store.Mutate(func(st *State) {
		st.FolderID = folder
		if recreated { // folder vanished: re-upload everything, never delete local
			for _, n := range st.Notes {
				n.DriveID, n.RemoteVersion = "", ""
			}
			st.Deleted = nil
		}
	})

	// 1. Trash notes deleted locally.
	var deleted []string
	s.store.View(func(st *State) { deleted = append(deleted, st.Deleted...) })
	for _, id := range deleted {
		if e := r.Trash(id); e != nil && !errors.Is(e, ErrNotFound) {
			return changed, e
		}
		s.store.Mutate(func(st *State) { st.Deleted = remove(st.Deleted, id) })
	}

	// 2. Pull remote changes.
	files, err := r.List(folder)
	if err != nil {
		return changed, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.ID] = true
		var known bool
		var ver string
		s.store.View(func(st *State) {
			if n := findByDrive(st, f.ID); n != nil {
				known, ver = true, n.RemoteVersion
			}
		})
		if known && ver == f.Version {
			continue
		}
		body, e := r.Export(f.ID)
		if e != nil {
			return changed, e
		}
		body = canon(body)
		s.store.Mutate(func(st *State) {
			n := findByDrive(st, f.ID)
			if n == nil {
				st.Notes = append(st.Notes, &Note{
					ID: newID(), Body: body, Pinned: f.Pinned, Updated: nowMs(), Fmt: "html",
					DriveID: f.ID, RemoteVersion: f.Version, BaseLocal: body, BaseRemote: body, SyncedPinned: f.Pinned,
				})
				changed = true
				return
			}
			remoteEdited := body != canon(n.BaseRemote)
			switch {
			case !remoteEdited:
				// only a version bump (e.g. Docs re-saving after import)
			case !n.localEdited():
				n.Body, n.BaseLocal, n.Updated, changed = body, body, nowMs(), true
			case canon(n.Body) == body:
				n.BaseLocal = n.Body
			default:
				// Both sides edited: keep local in this doc, save the Drive
				// version as a separate note so nothing is lost.
				st.Notes = append(st.Notes, &Note{
					ID: newID(), Fmt: "html", Updated: nowMs(),
					Body: body + "<p><i>(conflicting copy from Google Drive)</i></p>",
				})
				changed = true
			}
			if n.Pinned == n.SyncedPinned && n.Pinned != f.Pinned {
				n.Pinned, changed = f.Pinned, true
			}
			n.RemoteVersion, n.BaseRemote, n.SyncedPinned = f.Version, body, f.Pinned
		})
	}

	// 3. Notes whose doc was deleted/moved away on Drive.
	s.store.Mutate(func(st *State) {
		keep := st.Notes[:0]
		for _, n := range st.Notes {
			if n.DriveID != "" && !seen[n.DriveID] {
				if !n.localEdited() {
					changed = true
					continue // deleted remotely, no local edits: delete
				}
				n.DriveID, n.RemoteVersion = "", "" // local edits: re-upload
			}
			keep = append(keep, n)
		}
		st.Notes = keep
	})

	// 4. Push local changes.
	type push struct {
		id, driveID, body string
		pinned            bool
	}
	var pushes []push
	s.store.View(func(st *State) {
		for _, n := range st.Notes {
			if n.dirty() && strings.TrimSpace(htmlToText(n.Body)) != "" {
				pushes = append(pushes, push{n.ID, n.DriveID, n.Body, n.Pinned})
			}
		}
	})
	for _, p := range pushes {
		var rf RemoteFile
		var e error
		if p.driveID == "" {
			rf, e = r.Create(folder, titleOf(p.body), canon(p.body), p.pinned)
		} else {
			rf, e = r.Update(p.driveID, titleOf(p.body), canon(p.body), p.pinned)
			if errors.Is(e, ErrNotFound) {
				rf, e = r.Create(folder, titleOf(p.body), canon(p.body), p.pinned)
			}
		}
		if e != nil {
			return changed, e
		}
		// Record how Docs stored it, so its own reformatting isn't seen as an edit.
		remote, _ := r.Export(rf.ID)
		s.store.Mutate(func(st *State) {
			if n := findNote(st, p.id); n != nil {
				n.DriveID, n.RemoteVersion = rf.ID, rf.Version
				n.BaseLocal, n.BaseRemote, n.SyncedPinned = p.body, canon(remote), p.pinned
			} else {
				st.Deleted = append(st.Deleted, rf.ID) // deleted while uploading
			}
		})
	}
	return changed, nil
}

func remove(list []string, v string) []string {
	out := list[:0]
	for _, x := range list {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
