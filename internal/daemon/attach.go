package daemon

import (
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/nadavushka-dev/mailai/internal/proto"
	"github.com/nadavushka-dev/mailai/internal/stash"
	"github.com/nadavushka-dev/mailai/internal/store"
)

// MaxAttachments per message (SPEC §6).
const MaxAttachments = 8

// prepareAttachments validates what a sender attached and fills in what the
// daemon can see now: size, media type, whether the stash owns it. Nothing a
// client says about an attachment is kept; the path is re-examined.
func (d *Daemon) prepareAttachments(in []store.Attachment) ([]store.Attachment, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxAttachments {
		return nil, proto.Errorf(proto.CodeBadRequest, "at most %d attachments", MaxAttachments)
	}
	out := make([]store.Attachment, 0, len(in))
	for _, a := range in {
		if !filepath.IsAbs(a.Path) {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachment %q is not an absolute path", a.Path)
		}
		st, err := os.Stat(a.Path)
		if err != nil {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachment %s: %v", a.Path, err)
		}
		if st.IsDir() {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachment %s is a directory", a.Path)
		}
		mt := mime.TypeByExtension(filepath.Ext(a.Path))
		if i := strings.IndexByte(mt, ';'); i >= 0 {
			mt = mt[:i]
		}
		kind := "file"
		if strings.HasPrefix(mt, "image/") {
			kind = "image"
		}
		if !st.Mode().IsRegular() {
			return nil, proto.Errorf(proto.CodeBadRequest, "attachment %s is not a regular file", a.Path)
		}
		out = append(out, store.Attachment{
			Kind: kind, Path: a.Path, MediaType: mt, Bytes: st.Size(), ModTime: st.ModTime().UnixNano(),
			Stashed: d.stash != nil && d.stash.Owns(a.Path),
		})
	}
	return out, nil
}

// resolveMissing stamps Missing on attachments whose file is gone, and
// Changed on those modified since they were sent, at read time (SPEC §6:
// never trusted from send time).
func resolveMissing(ms []store.Message) {
	for i := range ms {
		resolveFiles(ms[i].Attachments)
	}
}

func resolveFiles(as []store.Attachment) {
	for j := range as {
		a := &as[j]
		st, err := os.Stat(a.Path)
		if err != nil || !st.Mode().IsRegular() {
			a.Missing = true
			continue
		}
		if st.Size() != a.Bytes || (a.ModTime != 0 && st.ModTime().UnixNano() != a.ModTime) {
			a.Changed = true
		}
	}
}

// Stash verbs.

type StashPutArgs struct {
	Session string `json:"session,omitempty"`
	Path    string `json:"path"`
}

type StashPutResult struct {
	Stored string `json:"stored"`
}

type StashGetArgs struct {
	Stored string `json:"stored"`
	Dest   string `json:"dest"`
}

type StashListArgs struct {
	Session string `json:"session,omitempty"`
}

// DefaultSession is used until adapters bring real sessions (milestone 3).
const DefaultSession = "default"

func (d *Daemon) stashVerb(verb string, raw []byte) (any, error) {
	if d.stash == nil {
		return nil, proto.Errorf(proto.CodeInternal, "this daemon has no stash directory")
	}
	switch verb {
	case "stash-put":
		a, err := decode[StashPutArgs](raw)
		if err != nil {
			return nil, err
		}
		if a.Session == "" {
			a.Session = DefaultSession
		}
		stored, err := d.stash.Put(a.Session, a.Path)
		if err != nil {
			return nil, proto.Errorf(proto.CodeBadRequest, "%v", err)
		}
		return StashPutResult{Stored: stored}, nil
	case "stash-get":
		a, err := decode[StashGetArgs](raw)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(a.Dest) {
			return nil, proto.Errorf(proto.CodeBadRequest, "dest must be an absolute path")
		}
		if err := d.stash.Get(a.Stored, a.Dest); err != nil {
			return nil, proto.Errorf(proto.CodeNotFound, "%v", err)
		}
		return map[string]string{"dest": a.Dest}, nil
	case "stash-list":
		a, err := decode[StashListArgs](raw)
		if err != nil {
			return nil, err
		}
		if a.Session == "" {
			a.Session = DefaultSession
		}
		list, err := d.stash.List(a.Session)
		if err != nil {
			return nil, proto.Errorf(proto.CodeBadRequest, "%v", err)
		}
		if list == nil {
			list = []stash.Entry{}
		}
		return map[string]any{"files": list}, nil
	}
	return nil, proto.Errorf(proto.CodeUnknownVerb, "%q", verb)
}
