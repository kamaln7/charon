package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

// pollWait is how long each long-poll asks the server to hold. The server
// caps it (max_wait_seconds in /api/config); asking for more is harmless.
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
	handle := tokenOf(created.RetrieveURL)
	fmt.Fprintf(stdout, "LINK %s\nEXPIRES %s\nHANDLE %s\n", created.SubmitURL, created.ExpiresAt, handle)
	if !await {
		return nil
	}
	return cmdAwait(c, stdout, stderr, handle, timeout, false, cleanup)
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
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fail(exitTimeout, "timed out after %s waiting for %s", timeout, handle)
		}
		view, err := c.view(handle, min(pollWait, remaining))
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
	if err := os.WriteFile(to, b, os.FileMode(perm)); err != nil {
		return err
	}
	// WriteFile honours the umask on creation and leaves an existing file's
	// mode alone; chmod makes --mode mean what it says either way.
	if err := os.Chmod(to, os.FileMode(perm)); err != nil {
		return err
	}
	return nil
}

func cmdCleanup(handle string, after time.Duration) error {
	dir, err := receiptDir(handle)
	if err != nil {
		return err
	}
	time.Sleep(after)
	return os.RemoveAll(dir)
}

// scheduleCleanup re-executes this binary detached, so the receipt disappears
// even if the shell that ran await is long gone.
func scheduleCleanup(handle string, after time.Duration) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "cleanup", "--after", after.String(), handle)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
