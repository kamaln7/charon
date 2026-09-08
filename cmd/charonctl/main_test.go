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

// testMain exists so that the deferred cleanup runs: os.Exit skips defers,
// which would leave the server child running and the temp dir behind.
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
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	os.Setenv("CHARON_SCRATCH_DIR", filepath.Join(tmp, "receipts"))
	os.MkdirAll(os.Getenv("CHARON_SCRATCH_DIR"), 0o700)
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
	t.Setenv("CHARON_API", "")
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)
	if err := os.MkdirAll(filepath.Join(configDir, "charonctl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "charonctl", "config.json"), []byte(`{"api":"`+serverURL+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := `{"title":"deploy creds","secrets":[
		{"name":"API_TOKEN","description":"rw"},
		{"name":"DEPLOY_KEY","type":"file"},
		{"name":"OPTIONAL_NOTE"}]}`
	out, _, err := runCtl(t, spec, "request")
	if err != nil {
		t.Fatal(err)
	}
	link, handle := line(out, "LINK"), line(out, "RETRIEVE_HANDLE")
	if link == "" || handle == "" || line(out, "EXPIRES") == "" || line(out, "MANAGE_HANDLE") == "" || line(out, "TITLE") == "" {
		t.Fatalf("request output missing fields:\n%s", out)
	}

	// Awkward value on purpose: quotes, spaces, a dollar sign, a newline.
	token := "it's $HOME \"quoted\"\nline2"
	go func() {
		time.Sleep(200 * time.Millisecond)
		fillAndSubmit(t, link, map[int]string{0: token}, map[int]string{1: "PRIVATE KEY BYTES"})
	}()

	start := time.Now()
	stdout, stderr, err := runCtl(t, "", "await", "--timeout", "10s", "--env", "--cleanup-after", "0", "--retrieve-handle", handle)
	if err != nil {
		t.Fatal(err, stderr)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("await took %v; long-poll is not waking on submit", time.Since(start))
	}
	// stderr says what happened, without values.
	for _, want := range []string{"set API_TOKEN (", "file DEPLOY_KEY (id_ed25519, 17 bytes)", "blank OPTIONAL_NOTE"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "PRIVATE") || strings.Contains(stderr, "quoted") {
		t.Errorf("stderr leaked a value:\n%s", stderr)
	}
	// stdout is eval-safe: run it through a real shell and read the result back.
	script := stdout + `printf '%s' "$API_TOKEN" > "$OUT/tok"; cp "$DEPLOY_KEY_FILE" "$OUT/key"; [ -z "${OPTIONAL_NOTE+x}" ]`
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
	stdout2, _, err := runCtl(t, "", "await", "--env", "--cleanup-after", "0", "--retrieve-handle", handle)
	if err != nil || stdout2 != stdout {
		t.Errorf("second await: err=%v, same output=%v", err, stdout2 == stdout)
	}

	// get: to stdout, to a file with a mode, and blank as its own exit code.
	got, _, err := runCtl(t, "", "get", "--retrieve-handle", handle, "--name", "API_TOKEN")
	if err != nil || got != token {
		t.Errorf("get API_TOKEN = %q, %v", got, err)
	}
	keyPath := filepath.Join(t.TempDir(), "k")
	if _, _, err := runCtl(t, "", "get", "--to", keyPath, "--mode", "0400", "--retrieve-handle", handle, "--name", "DEPLOY_KEY"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(keyPath); st.Mode().Perm() != 0o400 {
		t.Errorf("--mode 0400 gave %v", st.Mode().Perm())
	}
	_, _, err = runCtl(t, "", "get", "--retrieve-handle", handle, "--name", "OPTIONAL_NOTE")
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != exitBlank {
		t.Errorf("get on a blank field: err=%v, want exit %d", err, exitBlank)
	}

	if _, _, err := runCtl(t, "", "cleanup", "--retrieve-handle", handle); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReceipt(handle); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("receipt survived cleanup: %v", err)
	}
	_, _, err = runCtl(t, "", "get", "--retrieve-handle", handle, "--name", "API_TOKEN")
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
	view, err := newClient(serverURL).view(line(out, "RETRIEVE_HANDLE"), 0)
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
	_, _, err = runCtl(t, "", "await", "--timeout", "300ms", "--retrieve-handle", line(out, "RETRIEVE_HANDLE"))
	var ec exitCode
	if !errors.As(err, &ec) || ec.code != exitTimeout {
		t.Errorf("timeout: err=%v, want exit %d", err, exitTimeout)
	}
	_, _, err = runCtl(t, "", "await", "--timeout", "1s", "--retrieve-handle", "aaaaaaaaaaaaaaaaaaaaaaaa")
	if !errors.As(err, &ec) || ec.code != exitTimeout {
		t.Errorf("unknown handle: err=%v, want exit %d", err, exitTimeout)
	}
}

func TestSendRoundTrip(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	t.Setenv("CHARON_SEND_TOKEN", "from-env")
	keyFile := filepath.Join(t.TempDir(), "gen.pem")
	os.WriteFile(keyFile, []byte("PEM"), 0o600)
	textFile := filepath.Join(t.TempDir(), "token")
	os.WriteFile(textFile, []byte("from-file"), 0o600)
	extra := filepath.Join(t.TempDir(), "extra.pem")
	os.WriteFile(extra, []byte("EXTRA"), 0o600)
	spec := `{"title":"generated key","secrets":[
		{"name":"passphrase","text":"correct horse"},
		{"name":"key","type":"file","file":"` + keyFile + `"},
		{"name":"bundle","files":["` + keyFile + `","` + extra + `"]},
		{"name":"from_env","env":"CHARON_SEND_TOKEN"},
		{"name":"from_text_file","text_file":"` + textFile + `"}
	]}`
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
	if line(out, "TITLE") != "generated key" {
		t.Errorf("TITLE = %q", line(out, "TITLE"))
	}
	if got.Secrets[0].Text == nil || *got.Secrets[0].Text != "correct horse" {
		t.Errorf("text = %v", got.Secrets[0].Text)
	}
	if got.Secrets[1].Type != api.TypeFile || len(got.Secrets[1].Files) != 1 || got.Secrets[1].Files[0].Filename != "gen.pem" {
		t.Errorf("file = %+v", got.Secrets[1])
	}
	if got.Secrets[2].Type != api.TypeFile || len(got.Secrets[2].Files) != 2 {
		t.Errorf("files = %+v", got.Secrets[2])
	}
	if got.Secrets[3].Text == nil || *got.Secrets[3].Text != "from-env" {
		t.Errorf("env = %v", got.Secrets[3].Text)
	}
	if got.Secrets[4].Text == nil || *got.Secrets[4].Text != "from-file" {
		t.Errorf("text_file = %v", got.Secrets[4].Text)
	}
	manage := line(out, "MANAGE_HANDLE")
	if manage == "" {
		t.Fatalf("send omitted MANAGE_HANDLE:\n%s", out)
	}
	if _, err := c.retrieve(tokenOf(link)); err == nil {
		t.Fatal("second retrieve succeeded after linger 0")
	}
	for _, bad := range []string{
		`{"secrets":[{"name":"both","text":"a","file":"` + keyFile + `"}]}`,
		`{"secrets":[{"name":"neither"}]}`,
		`{"secrets":[{"name":"missing","file":"/nonexistent"}]}`,
		`{"secrets":[{"name":"missing_env","env":"CHARON_SEND_UNSET"}]}`,
		`{"secrets":[{"name":"missing_text_file","text_file":"/nonexistent"}]}`,
		`{"secrets":[{"name":"two","env":"CHARON_SEND_TOKEN","text":"x"}]}`,
		`{"secrets":[{"name":"typed","type":"file","text":"x"}]}`,
		`{"secrets":[{"name":"only_type","type":"file"}]}`,
	} {
		if _, _, err := runCtl(t, bad, "send"); err == nil {
			t.Errorf("send accepted %s", bad)
		}
	}
}

func TestAbortSendDestroysDraft(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	c := newClient(serverURL)
	created, err := c.create(api.KindSend, api.CreateRequest{Secrets: []api.SecretSpec{{Name: "T"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := abortSend(c, created.ManageID, fail(exitError, "boom")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("abortSend: %v", err)
	}
	if _, err := c.view(tokenOf(created.RetrieveURL), 0); err == nil {
		t.Fatal("draft survived abortSend")
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

// Names reach --env unquoted. A receipt from an entry charonctl did not create
// may carry any name the server accepted, so --env must refuse rather than
// hand eval a command.
func TestEnvRefusesUnsafeNames(t *testing.T) {
	dir := t.TempDir()
	r := &receipt{dir: dir, Secrets: []receiptSecret{{Name: "X=$(id) #", Type: api.TypeText}}}
	os.WriteFile(r.valuePath(0), []byte("v"), 0o600)
	if _, err := r.envLines(); err == nil {
		t.Fatal("envLines accepted an unsafe name")
	}
	// get still works: the name is only data there.
	if _, b, err := r.value("X=$(id) #"); err != nil || string(b) != "v" {
		t.Errorf("value = %q, %v", b, err)
	}
}

// Handles are base32 and may start with a digit; the receipt path check must
// accept every id charon can mint and nothing that could escape the directory.
func TestHandleShape(t *testing.T) {
	for _, ok := range []string{"4rdzmmbuqvs4tkzm7d3hcl4p", "abcdefghijklmnopqrstuvwx"} {
		if _, err := receiptDir(ok); err != nil {
			t.Errorf("rejected valid handle %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"../etc", "ABCDEFGHIJKLMNOPQRSTUVWX", "short", "abcdefghijklmnopqrstuvw1"} {
		if _, err := receiptDir(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestServerConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body, override, want string
		bad                        bool
	}{
		{name: "missing", want: "http://localhost:1337"},
		{name: "empty object", body: `{}`, want: "http://localhost:1337"},
		{name: "configured", body: `{"api":"https://config.example"}`, want: "https://config.example"},
		{name: "override", body: `{"api":"https://config.example"}`, override: "https://env.example", want: "https://env.example"},
		{name: "env only", override: "https://env.example", want: "https://env.example"},
		{name: "unknown field", body: `{"aip":"typo"}`, bad: true},
		{name: "malformed", body: `{`, bad: true},
		{name: "wrong type", body: `{"api":42}`, bad: true},
		{name: "null", body: `null`, bad: true},
		{name: "trailing JSON", body: `{} {}`, bad: true},
		{name: "trailing garbage", body: `{} !`, bad: true},
		{name: "override does not hide errors", body: `{`, override: "https://env.example", bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHARON_API", tc.override)
			dir := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", dir)
			if tc.body != "" {
				if err := os.Mkdir(filepath.Join(dir, "charonctl"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "charonctl", "config.json"), []byte(tc.body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			base, err := serverConfig()
			if (err != nil) != tc.bad {
				t.Fatalf("error = %v, want error %v", err, tc.bad)
			}
			if !tc.bad && newClient(base).base != tc.want {
				t.Fatalf("server = %q, want %q", newClient(base).base, tc.want)
			}
		})
	}
	t.Run("home fallback", func(t *testing.T) {
		t.Setenv("CHARON_API", "")
		home := t.TempDir()
		t.Setenv("HOME", home)
		dir := filepath.Join(home, ".config", "charonctl")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"api":"https://home.example"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, xdg := range []string{"", "relative/path"} {
			t.Setenv("XDG_CONFIG_HOME", xdg)
			base, err := serverConfig()
			if err != nil || base != "https://home.example" {
				t.Fatalf("XDG=%q: base=%q, err=%v", xdg, base, err)
			}
		}
	})
}

func TestCLIFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"await"}, "requires --retrieve-handle"},
		{[]string{"get", "--name", "TOKEN"}, "requires --retrieve-handle"},
		{[]string{"get", "--retrieve-handle", "aaaaaaaaaaaaaaaaaaaaaaaa"}, "requires --name"},
		{[]string{"cleanup"}, "requires --retrieve-handle"},
		{[]string{"destroy"}, "requires --manage-handle"},
		{[]string{"status"}, "exactly one of --retrieve-handle or --manage-handle"},
	} {
		_, _, err := runCtl(t, "", tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.args, err, tc.want)
		}
	}
	for _, verb := range []string{"request", "await", "get", "cleanup", "send", "status", "destroy"} {
		for _, arg := range []string{"unexpected", "--unknown"} {
			if _, _, err := runCtl(t, `{}`, verb, arg); err == nil {
				t.Errorf("%s accepted %s", verb, arg)
			}
		}
		if _, _, err := runCtl(t, "", verb, "--help"); err != nil {
			t.Errorf("%s help: %v", verb, err)
		}
	}
}

func TestAutomaticCleanup(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "charonctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	t.Setenv("CHARON_API", serverURL)
	out, _, err := runCtl(t, `{"secrets":[{"name":"TOKEN"}]}`, "request")
	if err != nil {
		t.Fatal(err)
	}
	handle := line(out, "RETRIEVE_HANDLE")
	fillAndSubmit(t, line(out, "LINK"), map[int]string{0: "test value"}, nil)
	cmd := exec.Command(bin, "await", "--retrieve-handle", handle, "--timeout", "5s", "--cleanup-after", "200ms")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("await: %v: %s", err, out)
	}
	if _, err := loadReceipt(handle); err != nil {
		t.Fatalf("receipt not created: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := loadReceipt(handle)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("automatic cleanup did not remove receipt: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCleanupHandleNotInArgvOrEnv(t *testing.T) {
	handle := "cccccccccccccccccccccccc"
	from, err := writeCleanupHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(from)
	raw, err := os.ReadFile(from)
	if err != nil || strings.TrimSpace(string(raw)) != handle {
		t.Fatalf("from file: %q, %v", raw, err)
	}
	cmd := cleanupCmd("/bin/true", time.Second, from)
	for _, a := range cmd.Args {
		if strings.Contains(a, handle) {
			t.Fatalf("handle in argv: %v", cmd.Args)
		}
	}
	for _, e := range cmd.Env {
		if strings.Contains(e, handle) {
			t.Fatalf("handle in env: %s", e)
		}
	}
}

func TestCleanupFromFile(t *testing.T) {
	handle := "cccccccccccccccccccccccc"
	dir, err := receiptDir(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	from, err := writeCleanupHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCtl(t, "", "cleanup", "--from", from); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("receipt survived --from cleanup")
	}
	if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("from file survived cleanup")
	}
}

func TestStatusAndWrongHandleRole(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	out, _, err := runCtl(t, `{"title":"status-me","secrets":[{"name":"X"}]}`, "request")
	if err != nil {
		t.Fatal(err)
	}
	retrieve, manage := line(out, "RETRIEVE_HANDLE"), line(out, "MANAGE_HANDLE")
	got, _, err := runCtl(t, "", "status", "--manage-handle", manage)
	if err != nil {
		t.Fatal(err)
	}
	if line(got, "TITLE") != "status-me" || line(got, "KIND") != "request" || line(got, "LINK") != line(out, "LINK") {
		t.Fatalf("manage status:\n%s", got)
	}
	if line(got, "FULFILLED") != "false" || line(got, "RETRIEVE_HANDLE") != retrieve {
		t.Fatalf("manage status fields:\n%s", got)
	}
	got, _, err = runCtl(t, "", "status", "--retrieve-handle", retrieve)
	if err != nil {
		t.Fatal(err)
	}
	if line(got, "LINK") != "" || line(got, "TITLE") != "status-me" {
		t.Fatalf("retrieve status should omit LINK:\n%s", got)
	}

	_, stderr, err := runCtl(t, "", "destroy", "--manage-handle", retrieve)
	if err == nil || !strings.Contains(err.Error(), "retrieve handle") {
		t.Fatalf("destroy with retrieve handle: err=%v stderr=%s", err, stderr)
	}
	_, _, err = runCtl(t, "", "await", "--timeout", "1s", "--retrieve-handle", manage)
	if err == nil || !strings.Contains(err.Error(), "manage handle") {
		t.Fatalf("await with manage handle: %v", err)
	}
}

func TestRequestAwaitOnlyFlags(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	for _, args := range [][]string{
		{"request", "--cleanup-after", "1m"},
		{"request", "--timeout", "1h"},
	} {
		_, _, err := runCtl(t, `{"secrets":[{"name":"X"}]}`, args...)
		if err == nil || !strings.Contains(err.Error(), "requires --await") {
			t.Errorf("%v: %v", args, err)
		}
	}
}

func TestSendLinger(t *testing.T) {
	t.Setenv("CHARON_API", serverURL)
	out, _, err := runCtl(t, `{"secrets":[{"name":"T","text":"v"}],"linger":"1s"}`, "send")
	if err != nil {
		t.Fatal(err)
	}
	view, err := newClient(serverURL).view(line(out, "MANAGE_HANDLE"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if view.LingerSeconds != 1 {
		t.Errorf("linger_seconds = %d, want 1", view.LingerSeconds)
	}
	if _, _, err := runCtl(t, "", "destroy", "--manage-handle", line(out, "MANAGE_HANDLE")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCtl(t, `{"secrets":[{"name":"T","text":"v"}],"linger":"1h"}`, "send"); err == nil {
		t.Fatal("send accepted linger over the server cap")
	}
}

func TestOldHandleFlag(t *testing.T) {
	_, _, err := runCtl(t, "", "await", "--handle", "aaaaaaaaaaaaaaaaaaaaaaaa")
	if err == nil || !strings.Contains(err.Error(), "--retrieve-handle or --manage-handle") {
		t.Fatalf("old --handle: %v", err)
	}
}
