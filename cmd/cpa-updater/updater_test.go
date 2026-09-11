package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s: got %q, error %v; want %q", path, got, err, want)
	}
}

func assertClean(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "update-tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func archiveBytes(t *testing.T, member string, kind byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	header := &tar.Header{Name: member, Mode: 0755, Typeflag: kind}
	if kind == tar.TypeReg {
		header.Size = int64(len("new binary"))
	}
	if err := tw.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tw.Write([]byte("new binary")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fixture(t *testing.T, choice string) (service, release, downloadFunc) {
	t.Helper()
	svc, err := selectService(choice)
	if err != nil {
		t.Fatal(err)
	}
	svc.dir = t.TempDir()
	mustWrite(t, filepath.Join(svc.dir, svc.binary), []byte("old binary"))
	mustWrite(t, filepath.Join(svc.dir, "config.yaml"), []byte("configuration"))
	mustWrite(t, filepath.Join(svc.dir, "usage.db"), []byte("database"))
	name, member := svc.archive("v1.2.3", runtime.GOARCH)
	archive := archiveBytes(t, member, tar.TypeReg)
	rel := release{
		Tag: "v1.2.3",
		Assets: []releaseAsset{
			{Name: name, URL: "fixture:archive"},
			{Name: "checksums.txt", URL: "fixture:checksums"},
		},
	}
	return svc, rel, func(_ context.Context, url, destination string) error {
		var data []byte
		switch url {
		case "fixture:archive":
			data = archive
		case "fixture:checksums":
			data = fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(archive), name)
		}
		return os.WriteFile(destination, data, 0600)
	}
}

func TestArchiveLayout(t *testing.T) {
	for _, tc := range []struct{ choice, arch, name, member string }{
		{"1", "amd64", "CLIProxyAPI_7.2.158-7ong.1_linux_amd64.tar.gz", "cli-proxy-api"},
		{"1", "arm64", "CLIProxyAPI_7.2.158-7ong.1_linux_aarch64.tar.gz", "cli-proxy-api"},
		{"2", "amd64", "CLIProxyAPI_7.2.158-7ong.1_linux_amd64.tar.gz", "cli-proxy-api"},
		{"2", "arm64", "CLIProxyAPI_7.2.158-7ong.1_linux_aarch64.tar.gz", "cli-proxy-api"},
		{"3", "amd64", "cpa-usage-keeper_v7.2.158-7ong.1_linux_amd64.tar.gz", "cpa-usage-keeper_v7.2.158-7ong.1_linux_amd64/cpa-usage-keeper"},
		{"3", "arm64", "cpa-usage-keeper_v7.2.158-7ong.1_linux_arm64.tar.gz", "cpa-usage-keeper_v7.2.158-7ong.1_linux_arm64/cpa-usage-keeper"},
		{"4", "amd64", "cpa-updater_linux_amd64.tar.gz", "cpa-updater"},
		{"4", "arm64", "cpa-updater_linux_arm64.tar.gz", "cpa-updater"},
	} {
		svc, err := selectService(tc.choice)
		if err != nil {
			t.Fatal(err)
		}
		name, member := svc.archive("v7.2.158-7ong.1", tc.arch)
		if name != tc.name || member != tc.member {
			t.Fatalf("archive layout: %q %q", name, member)
		}
	}
}

func TestUpdateLifecycle(t *testing.T) {
	for _, choice := range []string{"1", "2", "3"} {
		for _, mode := range []string{"success", "download-failure", "cancel", "checksum-failure", "incomplete-release"} {
			t.Run(choice+"/"+mode, func(t *testing.T) {
				svc, rel, download := fixture(t, choice)
				if mode == "incomplete-release" {
					rel.Assets = nil
				}
				work := filepath.Join(svc.dir, "update-tmp")
				if err := os.Mkdir(work, 0700); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(work, "old-partial-download"), []byte("partial"))
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				restarts := 0
				err := update(ctx, svc, rel, func(ctx context.Context, url, destination string) error {
					if _, err := os.Stat(filepath.Join(work, "old-partial-download")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("stale download was retained")
					}
					if err := download(ctx, url, destination); err != nil {
						return err
					}
					if url == "fixture:archive" && mode == "download-failure" {
						return errors.New("connection lost after partial write")
					}
					if url == "fixture:checksums" && mode == "cancel" {
						cancel()
					}
					if url == "fixture:checksums" && mode == "checksum-failure" {
						name, _ := svc.archive("v1.2.3", runtime.GOARCH)
						return os.WriteFile(destination, []byte(strings.Repeat("0", 64)+"  "+name), 0600)
					}
					return nil
				}, func(string) error { restarts++; return nil })
				if mode == "success" {
					if err != nil || restarts != 1 {
						t.Fatalf("success: %v, restarts %d", err, restarts)
					}
					assertContents(t, filepath.Join(svc.dir, svc.binary), "new binary")
					assertContents(t, filepath.Join(svc.dir, svc.binary+".previous"), "old binary")
				} else {
					if err == nil || restarts != 0 {
						t.Fatalf("failure: %v, restarts %d", err, restarts)
					}
					assertContents(t, filepath.Join(svc.dir, svc.binary), "old binary")
				}
				assertContents(t, filepath.Join(svc.dir, "config.yaml"), "configuration")
				assertContents(t, filepath.Join(svc.dir, "usage.db"), "database")
				assertClean(t, svc.dir)
			})
		}
	}
}

func TestUpdateRollback(t *testing.T) {
	svc, rel, download := fixture(t, "1")
	calls := 0
	err := update(context.Background(), svc, rel, download, func(string) error {
		calls++
		if calls == 1 {
			assertContents(t, filepath.Join(svc.dir, svc.binary), "new binary")
			return errors.New("new executable failed to start")
		}
		assertContents(t, filepath.Join(svc.dir, svc.binary), "old binary")
		return nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("rollback: %v, calls %d", err, calls)
	}
	assertClean(t, svc.dir)
}

func TestDirectoryLock(t *testing.T) {
	svc, rel, download := fixture(t, "1")
	locked, err := lockDirectory(svc.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Close()
	work := filepath.Join(svc.dir, "update-tmp")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(work, "active-download")
	mustWrite(t, partial, []byte("active"))
	err = update(context.Background(), svc, rel, download, func(string) error { t.Fatal("unexpected restart"); return nil })
	if err == nil {
		t.Fatal("concurrent update acquired lock")
	}
	assertContents(t, partial, "active")
}

func TestChecksum(t *testing.T) {
	dir := t.TempDir()
	archive, list := filepath.Join(dir, "archive"), filepath.Join(dir, "checksums")
	mustWrite(t, archive, []byte("data"))
	mustWrite(t, list, fmt.Appendf(nil, "%x *package.tar.gz\n", sha256.Sum256([]byte("data"))))
	if err := verifyChecksum(archive, list, "package.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(archive, list, "missing.tar.gz"); err == nil {
		t.Fatal("accepted missing checksum")
	}
}

func TestExtractBinary(t *testing.T) {
	for _, kind := range []byte{tar.TypeReg, tar.TypeSymlink} {
		dir := t.TempDir()
		archive := filepath.Join(dir, "archive")
		mustWrite(t, archive, archiveBytes(t, "package/tool", kind))
		err := extractBinary(archive, "package/tool", filepath.Join(dir, "binary"))
		if (err == nil) != (kind == tar.TypeReg) {
			t.Fatalf("kind %d: %v", kind, err)
		}
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, "archive")
	mustWrite(t, archive, archiveBytes(t, "../outside", tar.TypeReg))
	if err := extractBinary(archive, "expected", filepath.Join(dir, "binary")); err == nil {
		t.Fatal("accepted an unexpected member")
	}
}

func TestSelfUpdate(t *testing.T) {
	for _, mode := range []string{"success", "download-failure", "checksum-failure", "cancel", "wrong-member", "invalid-archive"} {
		t.Run(mode, func(t *testing.T) {
			svc := service{binary: "renamed-updater", repo: "owner/repo", dir: t.TempDir(), self: true}
			target := filepath.Join(svc.dir, svc.binary)
			mustWrite(t, target, []byte("old binary"))
			name, _ := svc.archive("v1.0.0", runtime.GOARCH)
			archive := archiveBytes(t, "cpa-updater", tar.TypeReg)
			if mode == "wrong-member" {
				archive = archiveBytes(t, "unexpected-name", tar.TypeReg)
			}
			if mode == "invalid-archive" {
				archive = []byte("invalid archive")
			}
			rel := release{
				Tag: "v1.0.0",
				Assets: []releaseAsset{
					{Name: name, URL: "fixture:binary"},
					{Name: "checksums.txt", URL: "fixture:checksums"},
				},
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := update(ctx, svc, rel, func(_ context.Context, url, dest string) error {
				var data []byte
				switch url {
				case "fixture:binary":
					data = archive
				case "fixture:checksums":
					data = fmt.Appendf(nil, "%x  %s\n", sha256.Sum256(archive), name)
					if mode == "checksum-failure" {
						data = []byte(strings.Repeat("0", 64) + "  " + name)
					}
				}
				if err := os.WriteFile(dest, data, 0600); err != nil {
					return err
				}
				if url == "fixture:binary" {
					if mode == "download-failure" {
						return errors.New("download interrupted")
					}
					if mode == "cancel" {
						cancel()
					}
				}
				return nil
			}, func(string) error { t.Fatal("self update restarted a service"); return nil })
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				assertContents(t, target, "new binary")
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0755 {
					t.Fatalf("executable permissions: %v, %v", info, err)
				}
			} else {
				if err == nil {
					t.Fatal("expected update failure")
				}
				assertContents(t, target, "old binary")
			}
			entries, err := os.ReadDir(svc.dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != svc.binary {
				t.Fatalf("self update left extra files: %v, %v", entries, err)
			}
		})
	}
}

func TestCommitSurvivesCancellation(t *testing.T) {
	svc, rel, download := fixture(t, "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := update(ctx, svc, rel, download, func(string) error {
		cancel()
		assertContents(t, filepath.Join(svc.dir, svc.binary), "new binary")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertClean(t, svc.dir)
}

func TestRollbackFailure(t *testing.T) {
	svc, rel, download := fixture(t, "1")
	calls := 0
	err := update(context.Background(), svc, rel, download, func(string) error {
		calls++
		return errors.New("service failed")
	})
	if err == nil || calls != 2 {
		t.Fatalf("got %v, restarts %d", err, calls)
	}
	assertContents(t, filepath.Join(svc.dir, svc.binary), "old binary")
	assertClean(t, svc.dir)
}

func TestRestartService(t *testing.T) {
	for _, mode := range []string{"success", "restart", "is-active"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n[ \"$1\" = \"$FAIL_COMMAND\" ] && exit 1\nexit 0\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("CALLS", calls)
			t.Setenv("FAIL_COMMAND", mode)
			err := restartService("cli-proxy-api")
			if (err == nil) != (mode == "success") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			want := "restart cli-proxy-api.service\n"
			if mode != "restart" {
				want += "is-active --quiet cli-proxy-api.service\n"
			}
			assertContents(t, calls, want)
		})
	}
}
