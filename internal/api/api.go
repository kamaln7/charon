// Package api holds the wire contract charon speaks: every request and
// response body and the enumerations they carry. The server and charonctl
// both import it, so the two cannot drift.
//
// Nothing here is built from map[string]any: the wire shape is the contract,
// so it lives in types.
package api

// Kind is cosmetic: it tells the frontend which side of the link the visitor is
// on. Both modes are the same machine underneath — an entry with a submit token
// and a retrieve token. In request mode you hand out the submit link; in send
// mode you fill it in yourself and hand out the retrieve link.
type Kind string

const (
	KindSend    Kind = "send"
	KindRequest Kind = "request"
)

type SecretType string

const (
	TypeText SecretType = "text"
	TypeFile SecretType = "file"
)

// ---------- requests ----------

// SecretSpec describes one requested or supplied secret. Every field is
// optional — an unnamed secret is numbered, and a description only helps when
// you are asking someone else for something.
type SecretSpec struct {
	Name        string     `json:"name,omitempty"`
	Description string     `json:"description,omitempty"`
	Type        SecretType `json:"type,omitempty"`
}

type CreateRequest struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	Secrets     []SecretSpec `json:"secrets"`
	TTL         string       `json:"ttl,omitempty"`
	// Linger is how long the payload stays readable after the first retrieve.
	// Empty or "0s" burns on the next lookup; the server caps the maximum.
	Linger      string `json:"linger,omitempty"`
	CallbackURL string `json:"callback_url,omitempty"`
}

type SetTextRequest struct {
	Text string `json:"text"`
}

// ---------- responses ----------

type CreateResponse struct {
	Title       string `json:"title"`
	SubmitURL   string `json:"submit_url"`
	RetrieveURL string `json:"retrieve_url"`
	// PollURL is the view endpoint under another name, so a caller reading the
	// response sees the flow: poll it until fulfilled, then POST RetrieveURL.
	PollURL string `json:"poll_url"`
	// ManageURL carries the owner token. Creation redirects here so the two
	// links survive a refresh instead of living only in page state.
	ManageURL string `json:"manage_url"`
	// DestroyURL is DELETE with the manage id. It is the API form of ManageURL,
	// which is an HTML page with the token in the query string.
	DestroyURL string `json:"destroy_url"`
	SubmitID   string `json:"submit_id"`
	RetrieveID string `json:"retrieve_id"`
	ManageID   string `json:"manage_id"`
	ExpiresAt  string `json:"expires_at"`
}

// EntryResponse is the non-secret view of an entry. Draft values appear only
// for the submit token; the retrieve side gets values by consuming the entry.
type EntryResponse struct {
	Kind          Kind             `json:"kind"`
	Role          string           `json:"role"`
	Title         string           `json:"title"`
	HTML          string           `json:"description_html,omitempty"`
	Secrets       []SecretResponse `json:"secrets"`
	Fulfilled     bool             `json:"fulfilled"`
	Retrieved     bool             `json:"retrieved"`
	ExpiresAt     string           `json:"expires_at"`
	LingerSeconds int              `json:"linger_seconds"`

	// Only populated for the manage role: the links the owner hands out.
	SubmitURL   string `json:"submit_url,omitempty"`
	RetrieveURL string `json:"retrieve_url,omitempty"`
}

type SecretResponse struct {
	Name  string     `json:"name"`
	HTML  string     `json:"description_html,omitempty"`
	Type  SecretType `json:"type"`
	Text  string     `json:"text,omitempty"`
	Files []string   `json:"files,omitempty"`
}

// RevealResponse is the payload. Text is inline; files are one-shot URLs,
// because base64-ing a key file into an agent's context is the wrong outcome.
type RevealResponse struct {
	Title       string           `json:"title"`
	Secrets     []RevealedSecret `json:"secrets"`
	DestructsAt string           `json:"destructs_at"`
}

// RevealedSecret always carries both value fields, null when the submitter
// skipped that one. A missing key and a skipped secret would otherwise look
// identical to a caller, so "did they answer?" stays answerable.
type RevealedSecret struct {
	Name  string         `json:"name"`
	Type  SecretType     `json:"type"`
	Text  *string        `json:"text"`
	Files []FileResponse `json:"files"`
}

type FileResponse struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

type ConfigResponse struct {
	MaxFileBytes  int64    `json:"max_file_bytes"`
	MaxTextBytes  int64    `json:"max_text_bytes"`
	MaxSecrets    int      `json:"max_secrets"`
	TTLOptions    []string `json:"ttl_options"`
	DefaultTTL    string   `json:"default_ttl"`
	LingerSeconds int      `json:"linger_seconds"`
	Callbacks     bool     `json:"callbacks"`
}

type UploadResponse struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type OKResponse struct {
	OK bool `json:"ok"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
