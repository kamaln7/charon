package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/kamaln7/charon/internal/api"
)

// A receipt is what await leaves on disk: one private directory per handle
// holding the collected values, so get can read them without another round
// trip and a second await is a no-op rather than a lost secret.
//
// Values are stored under their index, never their name. A directory listing
// therefore reveals how many secrets were asked for and nothing else.
type receipt struct {
	dir     string
	Title   string          `json:"title"`
	Secrets []receiptSecret `json:"secrets"`
}

type receiptSecret struct {
	Name     string         `json:"name"`
	Type     api.SecretType `json:"type"`
	Blank    bool           `json:"blank"`
	Filename string         `json:"filename,omitempty"` // the upload's original name
	Size     int64          `json:"size"`
}

const manifestName = "manifest.json"

// identifier is the shape a secret name must have. Names double as shell
// variable names under --env, so the check happens at request time, when the
// caller can still fix it, not at eval time in a shell.
var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// handleShape is charon's id format: 24 characters of lowercase base32. It is
// checked before the handle becomes part of a path.
var handleShape = regexp.MustCompile(`^[a-z2-7]{24}$`)

func receiptDir(handle string) (string, error) {
	if !handleShape.MatchString(handle) {
		return "", fmt.Errorf("handle %q is not a charon id", handle)
	}
	base := os.Getenv("CHARON_SCRATCH")
	if base == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		} else {
			base = filepath.Join(cache, "charonctl")
			if err := os.MkdirAll(base, 0o700); err != nil {
				return "", err
			}
		}
	}
	return filepath.Join(base, "charonctl-"+handle), nil
}

func loadReceipt(handle string) (*receipt, error) {
	dir, err := receiptDir(handle)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	r := &receipt{dir: dir}
	if err := json.Unmarshal(raw, r); err != nil {
		return nil, fmt.Errorf("receipt for %s is corrupt: %w", handle, err)
	}
	return r, nil
}

// storeReceipt writes the payload to a fresh 0700 directory. Files are
// downloaded here and now: the first retrieval starts charon's self-destruct
// timer, so leaving them for later is leaving them to expire.
//
// It builds in a temporary directory and renames into place, so a failure
// midway leaves nothing behind that a later run would mistake for a receipt.
func storeReceipt(c *client, handle string, payload api.RevealResponse) (*receipt, error) {
	dir, err := receiptDir(handle)
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".charonctl-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	r, err := writeReceipt(c, tmp, payload)
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	r.dir = dir
	return r, nil
}

func writeReceipt(c *client, dir string, payload api.RevealResponse) (*receipt, error) {
	var err error
	r := &receipt{dir: dir, Title: payload.Title}
	for i, sec := range payload.Secrets {
		rs := receiptSecret{Name: sec.Name, Type: sec.Type}
		var blob []byte
		switch {
		case sec.Type == api.TypeFile && len(sec.Files) > 0:
			for j, f := range sec.Files {
				if blob, err = c.download(f.URL); err != nil {
					return nil, fmt.Errorf("downloading %q: %w", sec.Name, err)
				}
				path := r.valuePath(i)
				if j > 0 {
					path += "." + strconv.Itoa(j)
				}
				if err := os.WriteFile(path, blob, 0o600); err != nil {
					return nil, err
				}
				if j == 0 {
					rs.Filename = f.Filename
					rs.Size = int64(len(blob))
				}
			}
		case sec.Text != nil:
			blob = []byte(*sec.Text)
			rs.Size = int64(len(blob))
			if err := os.WriteFile(r.valuePath(i), blob, 0o600); err != nil {
				return nil, err
			}
		default:
			rs.Blank = true
		}
		r.Secrets = append(r.Secrets, rs)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	return r, os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600)
}

func (r *receipt) valuePath(i int) string { return filepath.Join(r.dir, strconv.Itoa(i)) }

// value returns a secret's bytes by name. errBlank is a distinct outcome so
// get can exit with its own code: "they left it empty" is an answer, not a
// failure of the tool.
var errBlank = errors.New("left blank")

func (r *receipt) value(name string) (receiptSecret, []byte, error) {
	for i, sec := range r.Secrets {
		if sec.Name != name {
			continue
		}
		if sec.Blank {
			return sec, nil, errBlank
		}
		b, err := os.ReadFile(r.valuePath(i))
		return sec, b, err
	}
	return receiptSecret{}, nil, fmt.Errorf("no secret named %q", name)
}

// envLines renders the receipt as `export` statements for eval. Text secrets
// become NAME='value'; files become NAME_FILE='path' so a key never has to
// pass through a variable. Blank secrets are omitted, and reported on stderr
// by the caller, so `${NAME:?}` in the consuming script does the right thing.
//
// Names are checked here as well as at request time: the entry may have been
// created by something other than charonctl, and a name is interpolated
// unquoted, so an unchecked one would be command injection through eval.
func (r *receipt) envLines() (string, error) {
	var b strings.Builder
	for i, sec := range r.Secrets {
		if sec.Blank {
			continue
		}
		if !identifier.MatchString(sec.Name) {
			return "", fmt.Errorf("secret name %q is not a shell identifier; use get instead of --env", sec.Name)
		}
		if sec.Type == api.TypeFile {
			fmt.Fprintf(&b, "export %s_FILE=%s\n", sec.Name, shellQuote(r.valuePath(i)))
			continue
		}
		v, err := os.ReadFile(r.valuePath(i))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "export %s=%s\n", sec.Name, shellQuote(string(v)))
	}
	return b.String(), nil
}

// shellQuote wraps a value in single quotes, the one quoting form in which
// nothing but a single quote is special, and escapes those as '\”.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// summary is the stderr report: what was set, never the values themselves.
func (r *receipt) summary() string {
	var b strings.Builder
	for _, sec := range r.Secrets {
		switch {
		case sec.Blank:
			fmt.Fprintf(&b, "blank %s\n", sec.Name)
		case sec.Type == api.TypeFile:
			fmt.Fprintf(&b, "file %s (%s, %d bytes)\n", sec.Name, sec.Filename, sec.Size)
		default:
			fmt.Fprintf(&b, "set %s (%d chars)\n", sec.Name, sec.Size)
		}
	}
	return b.String()
}
