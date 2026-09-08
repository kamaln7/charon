package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

// pollWait is how long each long-poll asks the server to hold. The server
// caps it at a minute; asking for more is harmless.
const pollWait = 30 * time.Second

func cmdRequest(c *client, stdin io.Reader, stdout, stderr io.Writer, await bool, timeout, cleanup time.Duration) error {
	var req api.CreateRequest
	if err := decodeStrict(stdin, &req); err != nil {
		return err
	}
	if len(req.Secrets) == 0 {
		return fail(exitError, `a request needs at least one entry in "secrets"`)
	}
	for i := range req.Secrets {
		s := &req.Secrets[i]
		if s.Name == "" {
			s.Name = "SECRET_" + strconv.Itoa(i+1)
		}
		if !identifier.MatchString(s.Name) {
			return fail(exitError, "secrets[%d].name %q must be a shell identifier (letters, digits, _), it becomes an environment variable", i, s.Name)
		}
	}
	if req.TTL == "" {
		req.TTL = "1d"
	}
	created, err := c.create(api.KindRequest, req)
	if err != nil {
		return err
	}
	printCreated(stdout, created.Title, created.SubmitURL, created.ExpiresAt, created.RetrieveID, created.ManageID)
	if !await {
		return nil
	}
	return cmdAwait(c, stdout, stderr, created.RetrieveID, timeout, false, cleanup)
}

func printCreated(w io.Writer, title, link, expires, retrieve, manage string) {
	fmt.Fprintf(w, "TITLE %s\nLINK %s\nEXPIRES %s\n", title, link, expires)
	if retrieve != "" {
		fmt.Fprintf(w, "RETRIEVE_HANDLE %s\n", retrieve)
	}
	fmt.Fprintf(w, "MANAGE_HANDLE %s\n", manage)
}

func cmdDestroy(c *client, handle string) error {
	if _, err := viewAs(c, handle, "manage"); err != nil {
		return err
	}
	return c.destroy(handle)
}

func cmdStatus(c *client, stdout io.Writer, handle, wantRole string) error {
	view, err := viewAs(c, handle, wantRole)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "TITLE %s\nKIND %s\n", view.Title, view.Kind)
	if view.Role == "manage" {
		link := view.RetrieveURL
		if view.Kind == api.KindRequest && view.SubmitURL != "" {
			link = view.SubmitURL
		}
		fmt.Fprintf(stdout, "LINK %s\n", link)
	}
	fmt.Fprintf(stdout, "EXPIRES %s\nFULFILLED %t\nRETRIEVED %t\n",
		view.ExpiresAt, view.Fulfilled, view.Retrieved)
	if view.Role == "manage" && view.RetrieveURL != "" {
		fmt.Fprintf(stdout, "RETRIEVE_HANDLE %s\n", tokenOf(view.RetrieveURL))
	}
	return nil
}

func viewAs(c *client, handle, want string) (api.EntryResponse, error) {
	if !handleShape.MatchString(handle) {
		return api.EntryResponse{}, fmt.Errorf("handle %q is not a charon id", handle)
	}
	view, err := c.view(handle, 0)
	var ae *apiError
	if errors.As(err, &ae) && ae.status == 404 {
		return view, fail(exitTimeout, "entry %s not found (expired, destroyed, or already collected)", handle)
	}
	if err != nil {
		return view, err
	}
	if view.Role != want {
		return view, fail(exitError, "this is a %s handle; need a %s handle", view.Role, want)
	}
	return view, nil
}

// cmdAwait blocks until the entry is fulfilled, collects it once, and answers
// from the local receipt on every later call. Only the first call talks to
// charon after fulfilment, so the linger window never matters to a caller.
func cmdAwait(c *client, stdout, stderr io.Writer, handle string, timeout time.Duration, env bool, cleanup time.Duration) error {
	r, err := loadReceipt(handle)
	if errors.Is(err, os.ErrNotExist) {
		if r, err = collect(c, handle, timeout); err != nil {
			return err
		}
		if cleanup > 0 {
			if err := scheduleCleanup(handle, cleanup); err != nil {
				fmt.Fprintln(stderr, "warning: could not schedule cleanup:", err)
			}
		}
	} else if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "collected %q\n%sreceipt %s\n", r.Title, r.summary(), r.dir)
	if env {
		lines, err := r.envLines()
		if err != nil {
			return err
		}
		io.WriteString(stdout, lines)
	}
	return nil
}

func collect(c *client, handle string, timeout time.Duration) (*receipt, error) {
	deadline := time.Now().Add(timeout)
	view, err := viewAs(c, handle, "retrieve")
	if err != nil {
		return nil, err
	}
	for !view.Fulfilled {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fail(exitTimeout, "timed out after %s waiting for %s", timeout, handle)
		}
		start := time.Now()
		view, err = c.view(handle, min(pollWait, remaining))
		var ae *apiError
		if errors.As(err, &ae) && ae.status == 404 {
			return nil, fail(exitTimeout, "request %s has expired or was already collected", handle)
		}
		if err != nil {
			return nil, err
		}
		if view.Fulfilled {
			break
		}
		// A server without ?wait= answers at once; do not spin on it.
		if time.Since(start) < time.Second {
			time.Sleep(time.Second)
		}
	}
	payload, err := c.retrieve(handle)
	if err != nil {
		return nil, err
	}
	return storeReceipt(c, handle, payload)
}

func cmdGet(stdout io.Writer, handle, name, to, mode string) error {
	r, err := loadReceipt(handle)
	if errors.Is(err, os.ErrNotExist) {
		return fail(exitTimeout, "nothing collected for %s; run await first", handle)
	}
	if err != nil {
		return err
	}
	_, b, err := r.value(name)
	if errors.Is(err, errBlank) {
		return fail(exitBlank, "%s was left blank", name)
	}
	if err != nil {
		return err
	}
	if to == "" {
		_, err = stdout.Write(b)
		return err
	}
	perm, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return fail(exitError, "--mode %q is not octal", mode)
	}
	// Create private, fix the mode, then write: the bytes are never on disk
	// under a umask-widened mode, and an existing file is tightened first.
	f, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(os.FileMode(perm)); err != nil {
		return err
	}
	_, err = f.Write(b)
	return err
}

func cmdCleanup(handle, from string, after time.Duration) error {
	if from != "" {
		b, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		handle = strings.TrimSpace(string(b))
		defer os.Remove(from)
	}
	dir, err := receiptDir(handle)
	if err != nil {
		return err
	}
	time.Sleep(after)
	return os.RemoveAll(dir)
}

// scheduleCleanup re-executes this binary detached, so the receipt disappears
// even if the shell that ran await is long gone. The handle is written to a
// 0600 file and passed as --from so it appears in neither argv nor the
// environment (`ps` and `ps e` both miss it).
func scheduleCleanup(handle string, after time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	from, err := writeCleanupHandle(handle)
	if err != nil {
		return err
	}
	cmd := cleanupCmd(exe, after, from)
	if err := cmd.Start(); err != nil {
		os.Remove(from)
		return err
	}
	return cmd.Process.Release()
}

func writeCleanupHandle(handle string) (string, error) {
	dir, err := receiptDir(handle)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(filepath.Dir(dir), ".charonctl-")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(handle); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func cleanupCmd(exe string, after time.Duration, from string) *exec.Cmd {
	cmd := exec.Command(exe, "cleanup", "--after", after.String(), "--from", from)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	detach(cmd)
	return cmd
}
