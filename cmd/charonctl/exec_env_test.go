package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecEnvCommand(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "charonctl")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, out)
	}
	handle := "bbbbbbbbbbbbbbbbbbbbbbbb"
	dir, err := receiptDir(handle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	r := &receipt{dir: dir, Secrets: []receiptSecret{
		{Name: "CHARON_TEST_TOKEN", Type: "text"},
		{Name: "CHARON_TEST_KEY", Type: "file"},
		{Name: "CHARON_TEST_EXCLUDED", Type: "text"},
		{Name: "CHARON_TEST_BLANK", Type: "text", Blank: true},
	}}
	for i, value := range []string{"'\" $HOME $(exit 99)\n\n", "key bytes", "excluded"} {
		if err := os.WriteFile(r.valuePath(i), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, manifestName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHARON_TEST_TOKEN", "stale")
	t.Setenv("CHARON_TEST_INHERITED", "preserved")
	cmd := exec.Command(bin, "exec-env", "--retrieve-handle", handle, "--name", "CHARON_TEST_TOKEN", "--name", "CHARON_TEST_KEY", "--", "/bin/sh", "-c", `test "$CHARON_TEST_INHERITED" = preserved && test -z "${CHARON_TEST_EXCLUDED+x}" && cat "$CHARON_TEST_KEY_FILE" && printf '%s' "$CHARON_TEST_TOKEN"`)
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "key bytes'\" $HOME $(exit 99)\n\n" {
		t.Fatalf("round trip: %q, %v", out, err)
	}
	cmd = exec.Command(bin, "exec-env", "--retrieve-handle", handle, "--name", "CHARON_TEST_TOKEN", "--", "/bin/sh", "-c", "exit 17")
	if err := cmd.Run(); err == nil || cmd.ProcessState.ExitCode() != 17 {
		t.Fatalf("child exit: %v", err)
	}
	for _, names := range [][]string{nil, {"missing"}, {"CHARON_TEST_BLANK"}} {
		args := []string{"exec-env", "--retrieve-handle", handle}
		for _, name := range names {
			args = append(args, "--name", name)
		}
		args = append(args, "--", "/bin/sh", "-c", "printf CHILD_STARTED")
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err == nil || strings.Contains(string(out), "CHILD_STARTED") {
			t.Fatalf("invalid selection started child: %q, %v", out, err)
		}
	}
	r.Secrets = r.Secrets[:3]
	if values, err := r.execEnvironment(nil); err != nil || len(values) != 3 {
		t.Fatalf("default all: %v, %v", values, err)
	}
	if err := os.WriteFile(r.valuePath(0), []byte("a\x00b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.execEnvironment([]string{"CHARON_TEST_TOKEN"}); err == nil {
		t.Fatal("accepted NUL")
	}
	if err := os.WriteFile(r.valuePath(0), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.Secrets[0].Name = "CHARON_TEST_KEY_FILE"
	if _, err := r.execEnvironment(nil); err == nil {
		t.Fatal("accepted variable collision")
	}
}
