package daemon

import (
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// apiArtifact shows one attachment of one message to the person, read from
// where the file lives when it is opened: a reference, never a copy (SPEC
// §6). Only a path some message attached is ever opened; a file that is
// gone, or is no longer a regular file, is a 404.
func (d *Daemon) apiArtifact(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	i, err := strconv.Atoi(q.Get("i"))
	if err != nil || i < 0 {
		http.Error(w, "bad attachment index", http.StatusBadRequest)
		return
	}
	m, ok := d.st.Get(q.Get("mid"))
	if !ok || i >= len(m.Attachments) {
		http.NotFound(w, r)
		return
	}
	a := m.Attachments[i]
	f, err := os.Open(a.Path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	ct, runs := artifactType(a.Path, a.MediaType, f)
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	if runs {
		// An opaque origin: scripts run, but nothing reaches this page's
		// session or API (SameSite=Strict, no allow-same-origin).
		h.Set("Content-Security-Policy", "sandbox allow-scripts allow-popups allow-forms allow-modals allow-downloads")
	}
	disp := "inline"
	if q.Get("dl") == "1" {
		disp = "attachment"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": filepath.Base(a.Path)}))
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// rasterTypes open as themselves: a browser does not run anything in them.
var rasterTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/avif": true, "image/bmp": true, "image/x-icon": true, "image/vnd.microsoft.icon": true,
	"application/pdf": true,
}

// artifactType says what to serve a file as, and whether it is something a
// browser could run (and so must be sandboxed). Text the system has no type
// for (markdown, logs, diffs, source) reads as plain text.
func artifactType(path, mt string, f io.ReadSeeker) (string, bool) {
	if mt == "" {
		mt = mime.TypeByExtension(filepath.Ext(path))
		if i := strings.IndexByte(mt, ';'); i >= 0 {
			mt = mt[:i]
		}
	}
	if rasterTypes[mt] {
		return mt, false
	}
	switch {
	case mt == "text/html", mt == "image/svg+xml":
		return mt + chs(mt), true
	case strings.HasPrefix(mt, "text/") || strings.HasSuffix(mt, "json") || strings.HasSuffix(mt, "+xml") || mt == "application/xml":
		return "text/plain; charset=utf-8", true
	}
	// Unknown: plain text when it looks like text, else a download.
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	f.Seek(0, io.SeekStart)
	if strings.HasPrefix(http.DetectContentType(buf[:n]), "text/") {
		return "text/plain; charset=utf-8", true
	}
	if mt == "" {
		mt = "application/octet-stream"
	}
	return mt, true
}

func chs(mt string) string {
	if strings.HasPrefix(mt, "text/") {
		return "; charset=utf-8"
	}
	return ""
}
