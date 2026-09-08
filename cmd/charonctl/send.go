package main

import (
	"fmt"
	"io"
	"os"

	"github.com/kamaln7/charon/internal/api"
)

// sendSpec is the caller's side of a send: each secret carries its value
// inline ("text") or as a path to upload ("file"). The type is inferred from
// which one is present, so the caller never has to state it twice.
type sendSpec struct {
	Title       string       `json:"title,omitempty"`
	Description string       `json:"description,omitempty"`
	TTL         string       `json:"ttl,omitempty"`
	Secrets     []sendSecret `json:"secrets"`
}

type sendSecret struct {
	Name        string  `json:"name,omitempty"`
	Description string  `json:"description,omitempty"`
	Text        *string `json:"text,omitempty"`
	File        string  `json:"file,omitempty"`
}

func cmdSend(c *client, stdin io.Reader, stdout io.Writer) error {
	var spec sendSpec
	if err := decodeStrict(stdin, &spec); err != nil {
		return err
	}
	if len(spec.Secrets) == 0 {
		return fail(exitError, `a send needs at least one entry in "secrets"`)
	}
	req := api.CreateRequest{Title: spec.Title, Description: spec.Description, TTL: spec.TTL}
	if req.TTL == "" {
		req.TTL = "1d"
	}
	for i, s := range spec.Secrets {
		if (s.Text == nil) == (s.File == "") {
			return fail(exitError, `secrets[%d] needs exactly one of "text" or "file"`, i)
		}
		typ := api.TypeText
		if s.File != "" {
			typ = api.TypeFile
			if _, err := os.Stat(s.File); err != nil {
				return fail(exitError, "secrets[%d].file: %v", i, err)
			}
		}
		req.Secrets = append(req.Secrets, api.SecretSpec{Name: s.Name, Description: s.Description, Type: typ})
	}

	created, err := c.create(api.KindSend, req)
	if err != nil {
		return err
	}
	sid := tokenOf(created.SubmitURL)
	for i, s := range spec.Secrets {
		if s.Text != nil {
			if err := c.setText(sid, i, *s.Text); err != nil {
				return fmt.Errorf("secrets[%d]: %w", i, err)
			}
			continue
		}
		f, err := os.Open(s.File)
		if err != nil {
			return err
		}
		err = c.upload(sid, i, s.File, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("secrets[%d]: %w", i, err)
		}
	}
	if err := c.submit(sid); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "LINK %s\nEXPIRES %s\n", created.RetrieveURL, created.ExpiresAt)
	return nil
}
