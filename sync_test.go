package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDrive mimics Google Docs: stores text, bumps versions, and on export adds
// a BOM + CRLF like the real API does.
type fakeDrive struct {
	folder  string
	files   map[string]*fakeFile
	n       int
	trashed map[string]bool
}
type fakeFile struct {
	name, body string
	ver        int
	pinned     bool
}

func newFake() *fakeDrive {
	return &fakeDrive{files: map[string]*fakeFile{}, trashed: map[string]bool{}}
}
func (f *fakeDrive) EnsureFolder(k string) (string, bool, error) {
	if f.folder == "" {
		f.folder = "F1"
	}
	return f.folder, k != "" && k != f.folder, nil
}
func (f *fakeDrive) List(string) ([]RemoteFile, error) {
	var out []RemoteFile
	for id, x := range f.files {
		out = append(out, RemoteFile{id, x.name, fmt.Sprint(x.ver), x.pinned})
	}
	return out, nil
}
func (f *fakeDrive) Export(id string) (string, error) {
	x, ok := f.files[id]
	if !ok {
		return "", ErrNotFound
	}
	b := strings.ReplaceAll(x.body, "<b><i>", "<i><b>")
	b = strings.ReplaceAll(b, "</i></b>", "</b></i>")
	return sanitizeHTML(`<html><head><style>.c1{font-weight:700}</style></head><body class="doc-content">` + b + `</body></html>`), nil
}
func (f *fakeDrive) Create(_, name, body string, p bool) (RemoteFile, error) {
	f.n++
	id := fmt.Sprint("D", f.n)
	f.files[id] = &fakeFile{name, body, 1, p}
	return RemoteFile{id, name, "1", p}, nil
}
func (f *fakeDrive) Update(id, name, body string, p bool) (RemoteFile, error) {
	x, ok := f.files[id]
	if !ok {
		return RemoteFile{}, ErrNotFound
	}
	x.name, x.body, x.pinned = name, body, p
	x.ver++
	return RemoteFile{id, name, fmt.Sprint(x.ver), p}, nil
}
func (f *fakeDrive) Trash(id string) error {
	delete(f.files, id)
	f.trashed[id] = true
	return nil
}
func (f *fakeDrive) phoneEdit(id, body string) { f.files[id].body = body; f.files[id].ver++ }

func setup(t *testing.T) (*Store, *Syncer, *fakeDrive) {
	st := OpenStore(filepath.Join(t.TempDir(), "d.json"))
	sy := NewSyncer(st)
	return st, sy, newFake()
}

func body(st *Store, id string) string {
	var b string
	st.View(func(s *State) { b = findNote(s, id).Body })
	return b
}

func TestRoundTrip(t *testing.T) {
	st, sy, fd := setup(t)
	n := st.New()
	st.SetBody(n.ID, "<h1>Groceries</h1><p>☐ milk</p><p>☐ <b>eggs</b></p>")
	if _, err := sy.syncOnce(fd); err != nil {
		t.Fatal(err)
	}
	if len(fd.files) != 1 || fd.files["D1"].name != "Groceries" {
		t.Fatalf("not uploaded: %+v", fd.files)
	}
	// A second sync with no changes must not upload again.
	sy.syncOnce(fd)
	if fd.files["D1"].ver != 1 {
		t.Fatalf("re-uploaded unchanged note, ver=%d", fd.files["D1"].ver)
	}
	// Phone edit flows back.
	fd.phoneEdit("D1", "<h1>Groceries</h1><p>☑ milk</p><p>☐ eggs</p><p>☐ bread</p>")
	changed, _ := sy.syncOnce(fd)
	if !changed || body(st, n.ID) != "<h1>Groceries</h1><p>☑ milk</p><p>☐ eggs</p><p>☐ bread</p>" {
		t.Fatalf("pull failed: %q", body(st, n.ID))
	}
	if fd.files["D1"].ver != 2 {
		t.Fatalf("pulled note was pushed back needlessly")
	}
}

func TestVersionBumpWithoutContentChangeKeepsLocalEdits(t *testing.T) {
	st, sy, fd := setup(t)
	n := st.New()
	st.SetBody(n.ID, "<p><b><i>Todo</i></b></p>")
	sy.syncOnce(fd)
	fd.files["D1"].ver++ // Docs bumps version (e.g. async conversion)
	st.SetBody(n.ID, "<p><b><i>Todo</i></b></p><p>more</p>")
	sy.syncOnce(fd)
	if body(st, n.ID) != "<p><b><i>Todo</i></b></p><p>more</p>" || fd.files["D1"].body != "<p><b><i>Todo</i></b></p><p>more</p>" {
		t.Fatalf("local edit lost: local=%q remote=%q", body(st, n.ID), fd.files["D1"].body)
	}
	if len(st.List()) != 1 {
		t.Fatalf("spurious conflict copy")
	}
}

func TestConflictKeepsBoth(t *testing.T) {
	st, sy, fd := setup(t)
	n := st.New()
	st.SetBody(n.ID, "<p>A</p>")
	sy.syncOnce(fd)
	fd.phoneEdit("D1", "<p>A phone</p>")
	st.SetBody(n.ID, "<p>A laptop</p>")
	sy.syncOnce(fd)
	sy.syncOnce(fd)
	l := st.List()
	if len(l) != 2 {
		t.Fatalf("want 2 notes, got %d", len(l))
	}
	all := l[0].Body + "|" + l[1].Body
	if !strings.Contains(all, "A phone") || !strings.Contains(all, "A laptop") {
		t.Fatalf("lost a side: %s", all)
	}
	if len(fd.files) != 2 {
		t.Fatalf("conflict copy not uploaded")
	}
}

func TestDeletesBothWays(t *testing.T) {
	st, sy, fd := setup(t)
	a, b := st.New(), st.New()
	st.SetBody(a.ID, "<p>a</p>")
	st.SetBody(b.ID, "<p>b</p>")
	sy.syncOnce(fd)
	st.Delete(a.ID)
	sy.syncOnce(fd)
	if len(fd.files) != 1 {
		t.Fatalf("local delete not propagated")
	}
	for id := range fd.files {
		fd.Trash(id)
	}
	sy.syncOnce(fd)
	if len(st.List()) != 0 {
		t.Fatalf("remote delete not propagated")
	}
}

func TestNewDocFromPhoneAndPin(t *testing.T) {
	st, sy, fd := setup(t)
	fd.Create("F1", "Phone note", "<p>Phone note</p><p>hello</p>", false)
	sy.syncOnce(fd)
	l := st.List()
	if len(l) != 1 || l[0].Body != "<p>Phone note</p><p>hello</p>" {
		t.Fatalf("phone note missing: %+v", l)
	}
	st.SetPinned(l[0].ID, true)
	sy.syncOnce(fd)
	if !fd.files["D1"].pinned {
		t.Fatal("pin not pushed")
	}
}

func TestEmptyNotesNotUploaded(t *testing.T) {
	st, sy, fd := setup(t)
	st.New()
	sy.syncOnce(fd)
	if len(fd.files) != 0 {
		t.Fatal("empty note uploaded")
	}
}

func TestFolderRecreatedReuploads(t *testing.T) {
	st, sy, fd := setup(t)
	n := st.New()
	st.SetBody(n.ID, "<p>keep me</p>")
	sy.syncOnce(fd)
	fd.files = map[string]*fakeFile{} // user deleted the folder
	fd.folder = "F2"
	sy.syncOnce(fd)
	if len(st.List()) != 1 || len(fd.files) != 1 {
		t.Fatalf("notes lost when folder removed: local=%d remote=%d", len(st.List()), len(fd.files))
	}
}

func TestTitle(t *testing.T) {
	if titleOf("<p><br></p><p>  ☐ <b>Buy</b> milk </p><p>x</p>") != "Buy milk" || titleOf("  ") != "Untitled note" {
		t.Fatal(titleOf("\n\n  ☐ Buy milk \nx"))
	}
}

func TestDocsExportSanitize(t *testing.T) {
	src := `<html><head><meta content="text/html; charset=UTF-8" http-equiv="content-type"><style type="text/css">ol{margin:0;padding:0}.c3{font-weight:700;font-style:normal}.c5{font-style:italic}.c7{text-decoration:line-through}.c0{color:#000000;font-weight:400}.title{font-size:26pt}</style></head><body class="c4 doc-content"><p class="c2 title" id="h.x"><span class="c0">My list</span></p><h2 class="c6"><span>Today</span></h2><p class="c1"><span class="c0">Plain </span><span class="c3">bold</span><span class="c0">&nbsp;and </span><span class="c5">it</span></p><ul class="c8 lst-kix_a-0 start"><li class="c1 li-bullet-0"><span class="c0">one</span></li></ul><ul class="c8 lst-kix_a-0"><li class="c1 li-bullet-0"><span class="c7">two</span></li></ul><p class="c1"><span class="c0"></span></p><p class="c1"><a class="c9" href="https://www.google.com/url?q=x"><span>link</span></a></p></body></html>`
	got := sanitizeHTML(src)
	want := `<h1>My list</h1><h2>Today</h2><p>Plain <b>bold</b> and <i>it</i></p><ul><li>one</li><li><s>two</s></li></ul><p><br></p><p>link</p>`
	if got != want {
		t.Fatalf("\ngot  %s\nwant %s", got, want)
	}
	if htmlToText(got) != "My list\nToday\nPlain bold and it\none\ntwo\n\nlink" {
		t.Fatalf("text: %q", htmlToText(got))
	}
}

func TestMigratePlainText(t *testing.T) {
	if g := textToHTML("Hi <there>\n\n☐ x"); g != "<p>Hi &lt;there&gt;</p><p><br></p><p>☐ x</p>" {
		t.Fatal(g)
	}
}
