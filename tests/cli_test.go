package tests

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Actions supplies a prebuilt binary. Tests never compile the application.
func executable(t *testing.T) string {
	t.Helper()
	path := os.Getenv("CPA_UPDATER_BINARY")
	if path == "" {
		t.Fatal("set CPA_UPDATER_BINARY to the prebuilt executable")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func write(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func command(t *testing.T, binary string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, binary, args...)
}

func TestCLI(t *testing.T) {
	for _, tc := range []struct {
		arg, input, want string
		fail             bool
	}{
		{"--version", "", "cpa-updater", false},
		{"--help", "", "official-cpa", false},
		{"", "0\n", "更新器自身", false},
		{"unknown", "", "请选择", true},
	} {
		t.Run(tc.arg+tc.input, func(t *testing.T) {
			var args []string
			if tc.arg != "" {
				args = []string{tc.arg}
			}
			cmd := command(t, executable(t), args...)
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("output %s, error %v", out, err)
			}
		})
	}
}

// wget is replaced only in the test process PATH. Actual services stay untouched.
func selfFixture(t *testing.T) (string, []byte, []byte, []string) {
	t.Helper()
	root := t.TempDir()
	install := filepath.Join(root, "install")
	bin := filepath.Join(root, "tools")
	for _, dir := range []string{install, bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	old, err := os.ReadFile(executable(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(install, "my-updater")
	write(t, target, old, 0755)
	next := append(bytes.Clone(old), []byte("\nfixture release\n")...)
	write(t, filepath.Join(root, "new-binary"), next, 0600)
	asset := "cpa-updater_linux_" + runtime.GOARCH
	manifest, err := json.Marshal(map[string]any{
		"tag_name": "v999.0.0",
		"assets": []map[string]string{
			{"name": asset, "browser_download_url": "https://fixture.invalid/binary"},
			{"name": "checksums.txt", "browser_download_url": "https://fixture.invalid/checksums"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "manifest"), manifest, 0600)
	write(t, filepath.Join(root, "checksums"), fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(next), asset), 0600)
	if err := syscall.Mkfifo(filepath.Join(root, "pause"), 0600); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(bin, "wget"), []byte(`#!/bin/sh
set -eu
hsts=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --no-hsts) hsts=1 ;;
    -O) shift; dest=$1 ;;
    https://*) url=$1 ;;
  esac
  shift
done
[ "$hsts" = 1 ]
case "$url" in
  */releases/latest) cat "$FIXTURE/manifest" ;;
  */binary)
    cp "$FIXTURE/new-binary" "$dest"
    if [ "${PAUSE_DOWNLOAD:-0}" = 1 ]; then
      printf 'download-ready\n'
      read -r ignored < "$FIXTURE/pause"
    fi
    ;;
  */checksums) cp "$FIXTURE/checksums" "$dest" ;;
  *) exit 9 ;;
esac
`), 0755)
	write(t, filepath.Join(bin, "systemctl"), []byte("#!/bin/sh\nprintf 'unexpected service call' > \"$FIXTURE/service-called\"\nexit 99\n"), 0755)
	env := append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FIXTURE="+root)
	return target, old, next, env
}

func TestSelfReplacement(t *testing.T) {
	target, _, next, env := selfFixture(t)
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	cmd := command(t, link, "self")
	cmd.Dir = t.TempDir()
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, next) {
		t.Fatalf("replacement failed: %v", err)
	}
	if out, err := command(t, target, "--version").CombinedOutput(); err != nil {
		t.Fatalf("new binary: %s %v", out, err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("installation leftovers: %v %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(target)), "service-called")); !os.IsNotExist(err) {
		t.Fatal("self update called systemctl")
	}
}

func TestConfirmationDeclined(t *testing.T) {
	target, old, _, env := selfFixture(t)
	cmd := command(t, target)
	cmd.Env = env
	cmd.Stdin = strings.NewReader("4\nn\n")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "确认更新") {
		t.Fatalf("confirmation: %s %v", out, err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatalf("binary changed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 {
		t.Fatalf("confirmation left files: %v %v", entries, err)
	}
}

func TestDownloadCancellation(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			target, old, _, env := selfFixture(t)
			cmd := command(t, target, "self")
			cmd.Env = append(env, "PAUSE_DOWNLOAD=1")
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stdout)
			ready := false
			for scanner.Scan() {
				if scanner.Text() == "download-ready" {
					ready = true
					break
				}
			}
			if !ready {
				_ = cmd.Wait()
				t.Fatalf("download failed to start: %s", &stderr)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("canceled update succeeded")
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, old) {
				t.Fatalf("old binary changed: %v", err)
			}
			if _, err := os.Stat(target + ".update-tmp"); !os.IsNotExist(err) {
				t.Fatalf("partial download remains: %v", err)
			}
			// The next run also exercises recovery from a manually seeded crash leftover.
			if err := os.Mkdir(target+".update-tmp", 0700); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(target+".update-tmp", "stale"), []byte("partial"), 0600)
			retry := command(t, target, "self")
			retry.Env = env
			if out, err := retry.CombinedOutput(); err != nil {
				t.Fatalf("retry: %s %v", out, err)
			}
			if _, err := os.Stat(target + ".update-tmp"); !os.IsNotExist(err) {
				t.Fatalf("retry leftovers: %v", err)
			}
		})
	}
}
