package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type api struct {
	store *Store
	cfg   Config
}

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/config", a.getConfig)
	mux.HandleFunc("POST /api/requests", a.create(KindRequest))
	mux.HandleFunc("POST /api/secrets", a.create(KindSend))
	mux.HandleFunc("GET /api/e/{id}", a.view)
	mux.HandleFunc("PUT /api/e/{id}/text/{idx}", a.setText)
	mux.HandleFunc("POST /api/e/{id}/files/{idx}", a.addFile)
	mux.HandleFunc("DELETE /api/e/{id}/files/{idx}/{n}", a.dropFile)
	mux.HandleFunc("POST /api/e/{id}/submit", a.submit)
	mux.HandleFunc("POST /api/e/{id}/retrieve", a.retrieve)
	mux.HandleFunc("GET /api/f/{token}", a.download)
}

// ---------- wire types ----------

type itemSpec struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        ItemType `json:"type,omitempty"`
}

type createReq struct {
	Title       string     `json:"title"`
	Description string     `json:"description,omitempty"`
	Items       []itemSpec `json:"items"`
	TTL         string     `json:"ttl,omitempty"`
}

type createResp struct {
	SubmitURL   string `json:"submit_url"`
	RetrieveURL string `json:"retrieve_url"`
	TelegramURL string `json:"telegram_url,omitempty"`
	// PollURL is the same resource as the view endpoint. Named separately so
	// the flow is obvious to a caller reading the response: poll it until
	// "fulfilled" is true, then POST retrieve_url once.
	PollURL   string `json:"poll_url"`
	ExpiresAt string `json:"expires_at"`
}

type viewResp struct {
	Kind            Kind       `json:"kind"`
	Role            string     `json:"role"`
	Title           string     `json:"title"`
	DescriptionHTML string     `json:"description_html,omitempty"`
	Items           []itemView `json:"items"`
	Fulfilled       bool       `json:"fulfilled"`
	Retrieved       bool       `json:"retrieved"`
	ExpiresAt       string     `json:"expires_at"`
}

// itemView carries the draft back to the form so a reload — or Telegram killing
// the webview — resumes exactly where you left off. Never sent to the retrieve
// side, which gets values only by consuming the entry.
type itemView struct {
	Name            string   `json:"name"`
	DescriptionHTML string   `json:"description_html,omitempty"`
	Type            ItemType `json:"type"`
	Text            string   `json:"text,omitempty"`
	Files           []string `json:"files,omitempty"`
}

// ---------- handlers ----------

func (a *api) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"max_file_bytes": a.cfg.Limits.MaxFileBytes,
		"max_text_bytes": a.cfg.Limits.MaxTextBytes,
		"max_files":      a.cfg.Limits.MaxFiles,
		"ttl_options":    ttlOptions,
		"linger":         a.cfg.Limits.Linger.String(),
		"telegram":       a.cfg.Telegram.Configured(),
	})
}

// create builds an entry in either mode. The only difference is which of the
// two links you keep and which you hand out.
func (a *api) create(kind Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authorized(w, r) {
			return
		}
		var req createReq
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "invalid JSON: %v", err)
			return
		}
		e, err := a.newEntry(kind, req)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		a.store.Put(e)
		writeJSON(w, http.StatusCreated, createResp{
			SubmitURL:   a.cfg.BaseURL + "/e/" + e.SubmitID,
			RetrieveURL: a.cfg.BaseURL + "/e/" + e.RetrieveID,
			TelegramURL: a.cfg.Telegram.DirectLink(e.SubmitID),
			PollURL:     a.cfg.BaseURL + "/api/e/" + e.RetrieveID,
			ExpiresAt:   e.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
}

func (a *api) view(w http.ResponseWriter, r *http.Request) {
	e, role, ok := a.lookup(w, r)
	if !ok {
		return
	}
	roleName := "retrieve"
	if role == roleSubmit {
		roleName = "submit"
	}
	out := viewResp{
		Kind:            e.Kind,
		Role:            roleName,
		Title:           e.Title,
		DescriptionHTML: renderMarkdown(e.Description),
		Fulfilled:       e.Fulfilled,
		Retrieved:       !e.ConsumedAt.IsZero(),
		ExpiresAt:       e.ExpiresAt.UTC().Format(time.RFC3339),
	}
	for _, it := range e.Items {
		v := itemView{
			Name:            it.Name,
			DescriptionHTML: renderMarkdown(it.Description),
			Type:            it.Type,
		}
		if role == roleSubmit && !e.Fulfilled {
			v.Text = it.Text
			for _, f := range it.Files {
				v.Files = append(v.Files, f.Name)
			}
		}
		out.Items = append(out.Items, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) setText(w http.ResponseWriter, r *http.Request) {
	e, idx, ok := a.draftTarget(w, r)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	max := a.cfg.Limits.MaxTextBytes
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max+1024)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if int64(len(body.Text)) > max {
		fail(w, http.StatusRequestEntityTooLarge, "text exceeds %d bytes", max)
		return
	}
	if err := a.store.SetText(e, idx, body.Text); err != nil {
		fail(w, statusFor(err), "%v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) addFile(w http.ResponseWriter, r *http.Request) {
	e, idx, ok := a.draftTarget(w, r)
	if !ok {
		return
	}
	if a.countFiles(e) >= a.cfg.Limits.MaxFiles {
		fail(w, http.StatusBadRequest, "at most %d files", a.cfg.Limits.MaxFiles)
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, http.StatusBadRequest, "expected multipart/form-data")
		return
	}
	part, err := mr.NextPart()
	if err != nil {
		fail(w, http.StatusBadRequest, "no file part")
		return
	}
	defer part.Close()
	f, err := a.spool(part)
	if err != nil {
		fail(w, statusFor(err), "%v", err)
		return
	}
	a.store.AddFile(e, idx, f)
	writeJSON(w, http.StatusOK, map[string]any{"name": f.Name, "size": f.Size})
}

func (a *api) dropFile(w http.ResponseWriter, r *http.Request) {
	e, idx, ok := a.draftTarget(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		fail(w, http.StatusBadRequest, "bad index")
		return
	}
	if err := a.store.DropFile(e, idx, n); err != nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) submit(w http.ResponseWriter, r *http.Request) {
	e, role, ok := a.lookup(w, r)
	if !ok {
		return
	}
	if role != roleSubmit {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.store.Submit(e); err != nil {
		fail(w, http.StatusConflict, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// retrieve is also the polling endpoint for a patient caller: it answers 409
// until the other side submits. Hermes can either poll this directly or poll
// the view endpoint and call this once.
func (a *api) retrieve(w http.ResponseWriter, r *http.Request) {
	e, role, ok := a.lookup(w, r)
	if !ok {
		return
	}
	if role != roleRetrieve {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	items, err := a.store.Retrieve(e)
	if err != nil {
		fail(w, statusFor(err), "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"title":        e.Title,
		"items":        payload(a.cfg.BaseURL, items),
		"destructs_at": e.ConsumedAt.Add(a.cfg.Limits.Linger).UTC().Format(time.RFC3339),
	})
}

func (a *api) download(w http.ResponseWriter, r *http.Request) {
	f, err := a.store.TakeFile(r.PathValue("token"))
	if err != nil {
		fail(w, http.StatusNotFound, "not found")
		return
	}
	fh, err := openFile(f)
	if err != nil {
		fail(w, http.StatusGone, "file is gone")
		return
	}
	defer fh.Close()

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(f.Size, 10))
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename*=UTF-8''%s", urlEscape(f.Name)))
	io.Copy(w, fh)
}

// ---------- helpers ----------

func (a *api) lookup(w http.ResponseWriter, r *http.Request) (*Entry, role, bool) {
	if !a.authorized(w, r) {
		return nil, 0, false
	}
	e, role, err := a.store.Lookup(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "not found")
		return nil, 0, false
	}
	return e, role, true
}

// draftTarget resolves the common preconditions for every draft-editing route:
// a valid submit token, an unsubmitted entry, and an in-range item index.
func (a *api) draftTarget(w http.ResponseWriter, r *http.Request) (*Entry, int, bool) {
	e, role, ok := a.lookup(w, r)
	if !ok {
		return nil, 0, false
	}
	if role != roleSubmit {
		fail(w, http.StatusNotFound, "not found")
		return nil, 0, false
	}
	if e.Fulfilled {
		fail(w, http.StatusConflict, "already submitted")
		return nil, 0, false
	}
	idx, err := strconv.Atoi(r.PathValue("idx"))
	if err != nil || idx < 0 || idx >= len(e.Items) {
		fail(w, http.StatusBadRequest, "bad item index")
		return nil, 0, false
	}
	return e, idx, true
}

func (a *api) countFiles(e *Entry) int {
	n := 0
	for _, it := range e.Items {
		n += len(it.Files)
	}
	return n
}

func (a *api) newEntry(kind Kind, req createReq) (*Entry, error) {
	if strings.TrimSpace(req.Title) == "" {
		return nil, errors.New("title is required")
	}
	if len(req.Items) == 0 {
		return nil, errors.New("at least one item is required")
	}
	if len(req.Items) > a.cfg.Limits.MaxFiles {
		return nil, fmt.Errorf("at most %d items", a.cfg.Limits.MaxFiles)
	}
	ttl, err := parseTTL(req.TTL, a.cfg.Limits.MaxTTL)
	if err != nil {
		return nil, err
	}
	e := &Entry{
		Kind:        kind,
		Title:       req.Title,
		Description: req.Description,
		SubmitID:    NewID(),
		RetrieveID:  NewID(),
		ExpiresAt:   time.Now().Add(ttl),
	}
	for _, s := range req.Items {
		if strings.TrimSpace(s.Name) == "" {
			return nil, errors.New("every item needs a name")
		}
		t := s.Type
		if t != TypeFile {
			t = TypeText
		}
		e.Items = append(e.Items, Item{Name: s.Name, Description: s.Description, Type: t})
	}
	return e, nil
}

func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrFull):
		return http.StatusInsufficientStorage
	case errors.Is(err, ErrPending):
		return http.StatusConflict
	case errors.Is(err, ErrFulfilled):
		return http.StatusConflict
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}
