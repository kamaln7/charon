package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxRequestBody = 64 << 10

func (s *server) getConfig(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.cfg.configResponse())
	return nil
}

// create builds an entry in either mode. The only difference is which of the
// two links you keep and which you hand out.
func (s *server) create(kind Kind) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		var req CreateRequest
		if err := decodeJSON(w, r, &req); err != nil {
			return err
		}
		e, err := s.newEntry(kind, req)
		if err != nil {
			return err
		}
		if req.CallbackURL != "" {
			// Checked at creation so a caller learns straight away that its URL
			// is not permitted, rather than at delivery time when nobody is
			// listening for the error.
			if err := s.cfg.Callback.Check(req.CallbackURL); err != nil {
				return errorf(http.StatusBadRequest, "callback_url rejected: %v", err)
			}
			e.CallbackURL = req.CallbackURL
		}
		s.store.Put(e)
		writeJSON(w, http.StatusCreated, s.cfg.createResponse(e))
		return nil
	}
}

func (s *server) viewEntry(w http.ResponseWriter, r *http.Request) error {
	e, rl, err := s.entry(r)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, s.cfg.entryResponse(e, rl))
	return nil
}

func (s *server) setText(w http.ResponseWriter, r *http.Request) error {
	e, idx, err := s.draftTarget(r)
	if err != nil {
		return err
	}
	var req SetTextRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if int64(len(req.Text)) > s.cfg.Limits.MaxTextBytes {
		return errorf(http.StatusRequestEntityTooLarge, "text exceeds %d bytes", s.cfg.Limits.MaxTextBytes)
	}
	if err := s.store.SetText(e, idx, req.Text); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) addFile(w http.ResponseWriter, r *http.Request) error {
	e, idx, err := s.draftTarget(r)
	if err != nil {
		return err
	}
	if s.store.CountFiles(e) >= s.cfg.Limits.MaxFiles {
		return errorf(http.StatusBadRequest, "at most %d files", s.cfg.Limits.MaxFiles)
	}
	mr, err := r.MultipartReader()
	if err != nil {
		return errorf(http.StatusBadRequest, "expected multipart/form-data")
	}
	part, err := mr.NextPart()
	if err != nil {
		return errorf(http.StatusBadRequest, "no file part")
	}
	defer part.Close()

	// The file's encoded deadline is the latest moment it could still be
	// wanted: the entry's own expiry plus the post-retrieval linger.
	deadline := e.ExpiresAt.Add(s.cfg.Limits.Linger)
	f, err := s.scratch.Spool(part, deadline, s.cfg.Limits.MaxFileBytes, s.store.Reserve)
	if err != nil {
		return err
	}
	s.store.AddFile(e, idx, f)
	writeJSON(w, http.StatusOK, UploadResponse{Name: f.Name, Size: f.Size})
	return nil
}

func (s *server) dropFile(w http.ResponseWriter, r *http.Request) error {
	e, idx, err := s.draftTarget(r)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		return errorf(http.StatusBadRequest, "bad file index")
	}
	path, err := s.store.DropFile(e, idx, n)
	if err != nil {
		return errNotFound
	}
	s.scratch.Remove(path)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *server) submitEntry(w http.ResponseWriter, r *http.Request) error {
	e, err := s.entryAs(r, roleSubmit)
	if err != nil {
		return err
	}
	if err := s.store.Submit(e); err != nil {
		return err
	}
	if e.CallbackURL != "" {
		go s.cfg.Callback.Send(s.store, s.cfg.BaseURL, e)
	}
	writeJSON(w, http.StatusOK, OKResponse{OK: true})
	return nil
}

// retrieveEntry doubles as the polling endpoint: it answers 409 until the
// other side submits.
func (s *server) retrieveEntry(w http.ResponseWriter, r *http.Request) error {
	e, err := s.entryAs(r, roleRetrieve)
	if err != nil {
		return err
	}
	secrets, err := s.store.Retrieve(e)
	if err != nil {
		return err
	}
	destructs := e.ConsumedAt.Add(s.cfg.Limits.Linger)
	writeJSON(w, http.StatusOK, revealResponse(s.cfg.BaseURL, e.Title, destructs, secrets))
	return nil
}

func (s *server) downloadFile(w http.ResponseWriter, r *http.Request) error {
	f, err := s.store.TakeFile(r.PathValue("token"))
	if err != nil {
		return errNotFound
	}
	fh, err := f.Open()
	if err != nil {
		return errorf(http.StatusGone, "file is gone")
	}
	defer fh.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition", contentDisposition(f.Name))
	io.Copy(w, fh)
	return nil
}

// ---------- shared handler helpers ----------

// entry resolves the path's id to an entry and the capability it carries.
func (s *server) entry(r *http.Request) (*Entry, role, error) {
	e, rl, err := s.store.Lookup(r.PathValue("id"))
	if err != nil {
		return nil, 0, errNotFound
	}
	return e, rl, nil
}

// entryAs additionally requires a specific capability. A mismatch is a 404,
// not a 403: confirming that an ID exists but is the wrong kind would leak
// which links are live.
func (s *server) entryAs(r *http.Request, want role) (*Entry, error) {
	e, rl, err := s.entry(r)
	if err != nil {
		return nil, err
	}
	if rl != want {
		return nil, errNotFound
	}
	return e, nil
}

// draftTarget resolves the preconditions every draft-editing route shares: a
// valid submit token, an unsubmitted entry, and an in-range secret index.
func (s *server) draftTarget(r *http.Request) (*Entry, int, error) {
	e, err := s.entryAs(r, roleSubmit)
	if err != nil {
		return nil, 0, err
	}
	if e.Fulfilled {
		return nil, 0, ErrFulfilled
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 || idx >= len(e.Secrets) {
		return nil, 0, errorf(http.StatusBadRequest, "bad secret index")
	}
	return e, idx, nil
}

// newEntry fills in everything the caller left out. Only the secrets list is
// genuinely required: a missing title becomes a generated two-word name, and
// unnamed secrets are numbered in order.
func (s *server) newEntry(kind Kind, req CreateRequest) (*Entry, error) {
	limits := s.cfg.Limits
	if len(req.Secrets) == 0 {
		return nil, errorf(http.StatusBadRequest, "at least one secret is required")
	}
	if len(req.Secrets) > limits.MaxSecrets {
		return nil, errorf(http.StatusBadRequest, "at most %d secrets", limits.MaxSecrets)
	}
	ttl, err := parseTTL(req.TTL, limits)
	if err != nil {
		return nil, errorf(http.StatusBadRequest, "%v", err)
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = generatedName()
	}
	e := &Entry{
		Kind:        kind,
		Title:       title,
		Description: req.Description,
		SubmitID:    NewID(),
		RetrieveID:  NewID(),
		ManageID:    NewID(),
		ExpiresAt:   time.Now().Add(ttl),
	}
	for i, spec := range req.Secrets {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			name = fmt.Sprintf("secret-%d", i+1)
		}
		typ := spec.Type
		if typ != TypeFile {
			typ = TypeText
		}
		e.Secrets = append(e.Secrets, Secret{Name: name, Description: spec.Description, Type: typ})
	}
	return e, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(v); err != nil {
		return errorf(http.StatusBadRequest, "invalid JSON: %v", err)
	}
	return nil
}

func contentDisposition(name string) string {
	return fmt.Sprintf("attachment; filename*=UTF-8''%s", urlEscape(name))
}
