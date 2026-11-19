package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dbvfs/internal/state"
	"dbvfs/internal/storage"
	"dbvfs/internal/vfs"
)

// SessionInfo is what cd and pwd emit: the explicit session and its working
// directory, both stable strings (NFR-01).
type SessionInfo struct {
	Session string `json:"session"`
	Path    string `json:"path"`
}

// Resolve composes a relative path with the session's working directory. An
// empty raw string means the directory itself, which is how `ls --session ID`
// without PATH lists the cwd.
func (s Service) Resolve(session, raw string) (vfs.Path, error) {
	if session == "" {
		return vfs.Path{}, fmt.Errorf("%w: relative path %q requires --session", storage.ErrInvalid, raw)
	}
	sess, err := state.LoadSession(s.StateDir, session)
	if err != nil {
		return vfs.Path{}, err
	}
	if raw == "" {
		return vfs.Parse(sess.Cwd)
	}
	if strings.HasPrefix(raw, "/") {
		return vfs.Parse(raw)
	}
	joined := raw
	if sess.Cwd == "/" {
		joined = "/" + raw
	} else {
		joined = sess.Cwd + "/" + raw
	}
	return vfs.Parse(joined)
}

// CD validates that the destination is an existing container the policy allows
// and then persists it for this session (D-18).
func (s Service) CD(ctx context.Context, session string, p vfs.Path) (SessionInfo, error) {
	if err := s.validateContainer(ctx, p); err != nil {
		return SessionInfo{}, err
	}
	path, err := p.Canonical()
	if err != nil {
		return SessionInfo{}, err
	}
	if err := state.SaveSession(s.StateDir, session, path); err != nil {
		return SessionInfo{}, err
	}
	return SessionInfo{Session: session, Path: path}, nil
}

// PWD revalidates the stored working directory: a session whose alias was
// unmounted, or whose table or row disappeared, is E_STALE_SESSION (DESIGN §9).
func (s Service) PWD(ctx context.Context, session string) (SessionInfo, error) {
	sess, err := state.LoadSession(s.StateDir, session)
	if err != nil {
		return SessionInfo{}, err
	}
	p, err := vfs.Parse(sess.Cwd)
	if err != nil {
		return SessionInfo{}, fmt.Errorf("%w: session %q cwd %q is invalid", state.ErrStaleSession, session, sess.Cwd)
	}
	if err := s.validateContainer(ctx, p); err != nil {
		if errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrUnsupported) {
			return SessionInfo{}, fmt.Errorf("%w: session %q cwd %q no longer resolves", state.ErrStaleSession, session, sess.Cwd)
		}
		return SessionInfo{}, err
	}
	return SessionInfo{Session: session, Path: sess.Cwd}, nil
}

// validateContainer checks that p is a container (never a field), that it still
// exists and that the policy allows listing it.
func (s Service) validateContainer(ctx context.Context, p vfs.Path) error {
	switch p.Kind {
	case vfs.KindRoot:
		return nil
	case vfs.KindMount:
		m, err := s.mount(p.Alias)
		if err != nil {
			return err
		}
		if err := checkMountFile(m.File); err != nil {
			return err
		}
		if !readOpAllowed(m, "ls", mountPath(p.Alias)) {
			return policyDenied("ls", p.Alias, mountPath(p.Alias))
		}
		return nil
	case vfs.KindTable, vfs.KindRow:
		m, err := s.mount(p.Alias)
		if err != nil {
			return err
		}
		path, err := p.Canonical()
		if err != nil {
			return err
		}
		if !readOpAllowed(m, "ls", path) {
			return policyDenied("ls", lastSegment(path), path)
		}
		st, err := s.open(p.Alias)
		if err != nil {
			return err
		}
		defer st.Close()
		return st.Read(ctx, func(rd storage.Reader) error {
			t, err := findTable(ctx, rd, p.Table)
			if err != nil {
				return err
			}
			if !t.Readable {
				return fmt.Errorf("%w: table %q is not readable", storage.ErrUnsupported, t.Name)
			}
			if p.Kind == vfs.KindTable {
				return nil
			}
			if !t.Navigable {
				return fmt.Errorf("%w: table %q has no navigable primary key", storage.ErrUnsupported, t.Name)
			}
			key, err := resolveKey(p.Key, t)
			if err != nil {
				return err
			}
			_, err = rd.Get(ctx, t.Name, key)
			return err
		})
	}
	return fmt.Errorf("%w: cd destination must be a container (root, mount, table or row)", storage.ErrInvalid)
}
