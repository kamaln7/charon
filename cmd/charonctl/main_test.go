package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kamaln7/charon/internal/api"
)

// The tests run against the real server: TestMain builds it, starts it on a
// free port, and points every client at it. A fake would only prove the
// client agrees with itself.
var serverURL string

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

// testMain exists so that cleanup runs: os.Exit skips deferred calls, and a
// server child left holding stderr keeps `go test` waiting a minute.
func testMain(m *testing.M) int {
	tmp, err := os.MkdirTemp("", "charonctl-test-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "charon")
	if out, err := exec.Command("go", "build", "-o", bin, "../..").CombinedOutput(); err != nil {
		panic(string(out))
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	addr := l.Addr().String()
	l.Close()
	serverURL = "http://" + addr

	srv := exec.Command(bin)
	srv.Env = append(os.Environ(),
		"CHARON_ADDR="+addr,
		"CHARON_BASE_URL="+serverURL,
		"CHARON_SCRATCH_DIR="+filepath.Join(tmp, "scratch"),
		"CHARON_LINGER=2s",
	)
	if err := srv.Start(); err != nil {
		panic(err)
	}
	defer srv.Process.Kill()
	for i := 0; ; i++ {
		if resp, err := http.Get(serverURL + "/api/config"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 100 {
			panic("server did not come up")
		}
		time.Sleep(50 * time.Millisecond)
	}
	os.Setenv("CHARON_SCRATCH", filepath.Join(tmp, "receipts"))
	os.MkdirAll(os.Getenv("CHARON_SCRATCH"), 0o700)
	return m.Run()
}

func runCtl(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errb bytes.Buffer
	err = run(args, strings.NewReader(stdin), &out, &errb)
	return out.String(), errb.String(), err
}

func line(out, key string) string {
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, key+" "); ok {
			return v
		}
	}
	return ""
}

// fillAndSubmit plays the user: it uses the submit link the way the browser
// form would, through the same API the form uses.
func fillAndSubmit(t *testing.T, submitURL string, text map[int]string, files map[int]string) {
	t.Helper()
	c := newClient(serverURL)
	id := tokenOf(submitURL)
	for i, v := range text {
		if err := c.setText(id, i, v); err != nil {
			t.Fatal(err)
		}
	}
	for i, content := range files {
		if err := c.upload(id, i, "id_ed25519", strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.submit(id); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAwaitGetCleanup(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	spec := `{"title":"deploy creds","secrets":[
		{"name":"DO_TOKEN","description":"rw"},
		{"name":"DEPLOY_KEY","type":"file"},
		{"name":"OPTIONAL_NOTE"}]}`
	out, _, err := runCtl(t, spec, "request")
	if err != nil {
		t.Fatal(err)
	}
	link, handle := line(out, "LINK"), line(out, "HANDLE")
	if link == "" || handle == "" || line(out, "EXPIRES") == "" {
		t.Fatalf("request output missing fields:\n%s", out)
	}

	// Awkward value on purpose: quotes, spaces, a dollar sign, a newline.
	token := "it's $HOME \"quoted\"\nline2"
	go func() {
		time.Sleep(200 * time.Millisecond)
		fillAndSubmit(t, link, map[int]string{0: token}, map[int]string{1: "PRIVATE KEY BYTES"})
	}()

	start := time.Now()
	stdout, stderr, err := runCtl(t, "", "await", "--timeout", "10s", "--env", "--cleanup-after", "0", handle)
	if err != nil {
		t.Fatal(err, stderr)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("await took %v; long-poll is not waking on submit", time.Since(start))
	}
	// stderr says what happened, without values.
	for _, want := range []string{"set DO_TOKEN (", "file DEPLOY_KEY (id_ed25519, 17 bytes)", "blank OPTIONAL_NOTE"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "PRIVATE") || strings.Contains(stderr, "quoted") {
		t.Errorf("stderr leaked a value:\n%s", stderr)
	}
	// stdout is eval-safe: run it through a real shell and read the result back.
	script := stdout + `printf '%s' "$DO_TOKEN" > "$OUT/tok"; cp "$DEPLOY_KEY_FILE" "$OUT/key"; [ -z "${OPTIONAL_NOTE+x}" ]`
	outDir := t.TempDir()
	sh := exec.Command("sh", "-c", script)
	sh.Env = append(os.Environ(), "OUT="+outDir)
	if b, err := sh.CombinedOutput(); err != nil {
		t.Fatalf("shell rejected env lines: %v\n%s\n%s", err, b, stdout)
	}
	if got, _ := os.ReadFile(filepath.Join(outDir, "tok")); string(got) != token {
		t.Errorf("round-tripped token = %q, want %q", got, token)
	}
	if got, _ := os.ReadFile(filepath.Join(outDir, "key")); string(got) != "PRIVATE KEY BYTES" {
		t.Errorf("round-tripped key = %q", got)
	}

	// A second await answers from the receipt, even though charon has now
	// consumed the entry.
	stdout2, _, err := runCtl(t, "", "await", "--env", "--cleanup-after", "0", handle)
	if err != nil || stdout2 != stdout {
		t.Errorf("second await: err=%v, same output=%v", err, stdout2 == stdout)
	}

	// get: to stdout, to a file with a mode, and blank as its own exit code.
	got, _, err := runCtl(t, "", "get", handle, "DO_TOKEN")
	if err != nil || got != token {
		t.Errorf("get DO_TOKEN = %q, %v", got, err)
	}
	keyPath := filepath.Join(t.TempDir(), "k")
	if _, _, err := runCtl(t, "", "get", "--to", keyPath, "--mode", "0400", handle, "DEPLOY_KEY"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(keyPath); st.Mode().Perm() != 0o400 {
		t.Errorf("--mode 0400 gave %v", st.Mode().Perm())
	}
	_, _, err = runCtl(t, "", "get", handle, "OPTIONAL_NOTE")
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != exitBlank {
		t.Errorf("get on a blank field: err=%v, want exit %d", err, exitBlank)
	}

	if _, _, err := runCtl(t, "", "cleanup", handle); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReceipt(handle); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("receipt survived cleanup: %v", err)
	}
	_, _, err = runCtl(t, "", "get", handle, "DO_TOKEN")
	if !errors.As(err, &ec) || ec.code != exitTimeout {
		t.Errorf("get after cleanup: err=%v, want exit %d", err, exitTimeout)
	}
}

func TestRequestRejectsBadNamesAndUnknownFields(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	for _, spec := range []string{
		`{"secrets":[{"name":"has space"}]}`,
		`{"secrets":[{"name":"1starts_with_digit"}]}`,
		`{"secrets":[{"name":"ok"}],"tittle":"typo"}`,
		`{"secrets":[]}`,
	} {
		if _, _, err := runCtl(t, spec, "request"); err == nil {
			t.Errorf("request accepted %s", spec)
		}
	}
	// Unnamed secrets get identifier names, not charon's "secret-1".
	out, _, err := runCtl(t, `{"secrets":[{},{}]}`, "request")
	if err != nil {
		t.Fatal(err)
	}
	view, err := newClient(serverURL).view(line(out, "HANDLE"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if view.Secrets[0].Name != "SECRET_1" || view.Secrets[1].Name != "SECRET_2" {
		t.Errorf("default names = %q, %q", view.Secrets[0].Name, view.Secrets[1].Name)
	}
}

func TestAwaitTimeoutAndExpiry(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	out, _, err := runCtl(t, `{"secrets":[{"name":"X"}]}`, "request")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runCtl(t, "", "await", "--timeout", "300ms", line(out, "HANDLE"))
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != exitTimeout {
		t.Errorf("timeout: err=%v, want exit %d", err, exitTimeout)
	}
	_, _, err = runCtl(t, "", "await", "--timeout", "1s", "aaaaaaaaaaaaaaaaaaaaaaaa")
	if !errors.As(err, &ec) || ec.code != exitTimeout {
		t.Errorf("unknown handle: err=%v, want exit %d", err, exitTimeout)
	}
}

func TestSendRoundTrip(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	keyFile := filepath.Join(t.TempDir(), "gen.pem")
	os.WriteFile(keyFile, []byte("PEM"), 0o600)
	spec := `{"title":"generated key","secrets":[{"name":"passphrase","text":"correct horse"},{"name":"key","file":"` + keyFile + `"}]}`
	out, _, err := runCtl(t, spec, "send")
	if err != nil {
		t.Fatal(err)
	}
	link := line(out, "LINK")
	c := newClient(serverURL)
	got, err := c.retrieve(tokenOf(link))
	if err != nil {
		t.Fatal(err)
	}
	if got.Secrets[0].Text == nil || *got.Secrets[0].Text != "correct horse" {
		t.Errorf("text = %v", got.Secrets[0].Text)
	}
	if got.Secrets[1].Type != api.TypeFile || len(got.Secrets[1].Files) != 1 || got.Secrets[1].Files[0].Filename != "gen.pem" {
		t.Errorf("file = %+v", got.Secrets[1])
	}
	for _, bad := range []string{
		`{"secrets":[{"name":"both","text":"a","file":"` + keyFile + `"}]}`,
		`{"secrets":[{"name":"neither"}]}`,
		`{"secrets":[{"name":"missing","file":"/nonexistent"}]}`,
	} {
		if _, _, err := runCtl(t, bad, "send"); err == nil {
			t.Errorf("send accepted %s", bad)
		}
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"plain":   `'plain'`,
		"it's":    `'it'\''s'`,
		"$HOME\n": "'$HOME\n'",
		"":        `''`,
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}
