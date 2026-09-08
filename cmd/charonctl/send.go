package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kamaln7/charon/internal/api"
)

// sendSpec is the caller's side of a send. Each secret takes exactly one
// source: inline text, an environment variable, one or more files to upload,
// or a file whose contents become text. Sources are resolved before the
// entry is created. text_file is copied verbatim.
type sendSpec struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	TTL         string       `json:"ttl,omitempty"`
	Linger      string       `json:"linger,omitempty"`
	Secrets     []sendSecret `json:"secrets"`
}

type sendSecret struct {
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Type        api.SecretType `json:"type,omitempty"`
	Text        *string        `json:"text,omitempty"`
	Env         string         `json:"env,omitempty"`
	File        string         `json:"file,omitempty"`
	Files       []string       `json:"files,omitempty"`
	TextFile    string         `json:"text_file,omitempty"`
}

type resolvedSecret struct {
	Name, Description string
	Type              api.SecretType
	Text              *string
	Files             []string
}

func resolveSendSecret(i int, s sendSecret) (resolvedSecret, error) {
	n := 0
	if s.Text != nil {
		n++
	}
	if s.Env != "" {
		n++
	}
	if s.File != "" {
		n++
	}
	if len(s.Files) > 0 {
		n++
	}
	if s.TextFile != "" {
		n++
	}
	if n != 1 {
		return resolvedSecret{}, fail(exitError, `secrets[%d] needs exactly one of "text", "env", "file", "files", or "text_file"`, i)
	}
	if s.Type != "" && s.Type != api.TypeText && s.Type != api.TypeFile {
		return resolvedSecret{}, fail(exitError, `secrets[%d].type %q must be %q or %q`, i, s.Type, api.TypeText, api.TypeFile)
	}
	out := resolvedSecret{Name: s.Name, Description: s.Description, Type: api.TypeText}
	switch {
	case s.Text != nil:
		out.Text = s.Text
	case s.Env != "":
		v, ok := os.LookupEnv(s.Env)
		if !ok {
			return out, fail(exitError, "secrets[%d].env %q is not set", i, s.Env)
		}
		out.Text = &v
	case s.TextFile != "":
		b, err := os.ReadFile(s.TextFile)
		if err != nil {
			return out, fail(exitError, "secrets[%d].text_file: %v", i, err)
		}
		v := string(b)
		out.Text = &v
	case s.File != "":
		out.Type = api.TypeFile
		out.Files = []string{s.File}
	default:
		out.Type = api.TypeFile
		out.Files = s.Files
	}
	for _, path := range out.Files {
		if path == "" {
			return out, fail(exitError, `secrets[%d]: file path must not be empty`, i)
		}
		if _, err := os.Stat(path); err != nil {
			return out, fail(exitError, "secrets[%d].file: %v", i, err)
		}
	}
	if s.Type != "" && s.Type != out.Type {
		return out, fail(exitError, `secrets[%d].type is %q but the source is a %s secret`, i, s.Type, out.Type)
	}
	return out, nil
}

func cmdSend(c *client, stdin io.Reader, stdout io.Writer) error {
	var spec sendSpec
	if err := decodeStrict(stdin, &spec); err != nil {
		return err
	}
	if len(spec.Secrets) == 0 {
		return fail(exitError, `a send needs at least one entry in "secrets"`)
	}
	resolved := make([]resolvedSecret, 0, len(spec.Secrets))
	req := api.CreateRequest{Title: spec.Title, Description: spec.Description, TTL: spec.TTL, Linger: spec.Linger}
	if req.TTL == "" {
		req.TTL = "1d"
	}
	for i, s := range spec.Secrets {
		got, err := resolveSendSecret(i, s)
		if err != nil {
			return err
		}
		resolved = append(resolved, got)
		req.Secrets = append(req.Secrets, api.SecretSpec{Name: got.Name, Description: got.Description, Type: got.Type})
	}

	created, err := c.create(api.KindSend, req)
	if err != nil {
		return err
	}
	manage := created.ManageID
	sid := created.SubmitID
	for i, s := range resolved {
		for _, path := range s.Files {
			f, err := os.Open(path)
			if err != nil {
				return abortSend(c, manage, err)
			}
			err = c.upload(sid, i, path, f)
			f.Close()
			if err != nil {
				return abortSend(c, manage, fmt.Errorf("secrets[%d]: %w", i, err))
			}
		}
		if len(s.Files) > 0 {
			continue
		}
		if err := c.setText(sid, i, *s.Text); err != nil {
			return abortSend(c, manage, fmt.Errorf("secrets[%d]: %w", i, err))
		}
	}
	if err := c.submit(sid); err != nil {
		return abortSend(c, manage, err)
	}
	printCreated(stdout, created.Title, created.RetrieveURL, created.ExpiresAt, "", manage)
	return nil
}

func abortSend(c *client, manage string, err error) error {
	if manage == "" {
		return err
	}
	if derr := c.destroy(manage); derr != nil {
		return fmt.Errorf("%w (draft left live; destroy failed: %v)", err, derr)
	}
	return err
}
