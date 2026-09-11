package main

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func backupFixture(t *testing.T, binary, current, backup string) service {
	t.Helper()
	svc := service{binary: binary, dir: t.TempDir()}
	for suffix, version := range map[string]string{"": current, ".previous": backup} {
		args, output := "--version", version
		if binary == "cli-proxy-api" {
			args, output = "-config /dev/null -h", "CLIProxyAPI Version: "+version+", Commit: test"
		}
		script := "#!/bin/sh\n[ \"$*\" = '" + args + "' ] || exit 9\nprintf '%s\\n' '" + output + "'\n"
		if err := os.WriteFile(filepath.Join(svc.dir, binary+suffix), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(svc.dir, "config.yaml"), []byte("configuration"))
	mustWrite(t, filepath.Join(svc.dir, "usage.db"), []byte("database"))
	return svc
}

func assertBackupVersions(t *testing.T, svc service, current, backup string) {
	t.Helper()
	if got := installedVersion(svc); got != current {
		t.Fatalf("current = %s, want %s", got, current)
	}
	if got := executableVersion(svc, filepath.Join(svc.dir, svc.binary+".previous")); got != backup {
		t.Fatalf("backup = %s, want %s", got, backup)
	}
	assertContents(t, filepath.Join(svc.dir, "config.yaml"), "configuration")
	assertContents(t, filepath.Join(svc.dir, "usage.db"), "database")
	assertClean(t, svc.dir)
}

func TestManageBackup(t *testing.T) {
	for _, tc := range []struct{ binary, current, backup, relation string }{
		{"cli-proxy-api", "7.2.158-7ong.10", "7.2.158-7ong.9", "比当前旧，恢复将降级"},
		{"cli-proxy-api", "7.2.158-7ong.9", "7.2.158-7ong.10", "比当前新，恢复将升级"},
		{"cli-proxy-api", "7.2.159", "7.2.158", "比当前旧，恢复将降级"},
		{"cli-proxy-api", "7.2.158", "7.2.158-7ong.1", "切换官方/自维护来源"},
		{"cli-proxy-api", "7.2.158-7ong.1", "7.2.158", "切换官方/自维护来源"},
		{"cpa-usage-keeper", "v1.15.4", "v1.15.3", "比当前旧，恢复将降级"},
		{"cpa-usage-keeper", "v1.15.4", "v1.15.4", "与当前相同"},
	} {
		for _, remove := range []bool{false, true} {
			name := "restore/"
			if remove {
				name = "delete/"
			}
			t.Run(name+tc.current+"/"+tc.backup, func(t *testing.T) {
				svc := backupFixture(t, tc.binary, tc.current, tc.backup)
				var output strings.Builder
				calls := 0
				restart := func(binary string) error {
					calls++
					if binary != svc.binary || installedVersion(svc) != tc.backup {
						t.Fatal("restart must use the restored executable")
					}
					return nil
				}
				if err := manageBackup(svc, remove, bufio.NewScanner(strings.NewReader("y\n")), &output, restart); err != nil {
					t.Fatal(err)
				}
				for _, want := range []string{"当前版本: " + tc.current + "\n", "备份版本: " + tc.backup + " [" + tc.relation + "]"} {
					if !strings.Contains(output.String(), want) {
						t.Fatalf("missing %q in %s", want, &output)
					}
				}
				if remove {
					if calls != 0 {
						t.Fatal("deletion restarted service")
					}
					if _, err := os.Stat(filepath.Join(svc.dir, svc.binary+".previous")); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("backup remains: %v", err)
					}
					assertBackupVersions(t, svc, tc.current, "未知")
				} else {
					if calls != 1 {
						t.Fatalf("restart calls = %d", calls)
					}
					assertBackupVersions(t, svc, tc.backup, tc.current)
					if err := manageBackup(svc, false, bufio.NewScanner(strings.NewReader("y\n")), &output, func(string) error { return nil }); err != nil {
						t.Fatal(err)
					}
					assertBackupVersions(t, svc, tc.current, tc.backup)
				}
			})
		}
	}
}

func TestBackupRestoreFailure(t *testing.T) {
	for _, failAgain := range []bool{false, true} {
		svc := backupFixture(t, "cli-proxy-api", "7.2.158-7ong.2", "7.2.158-7ong.1")
		var output strings.Builder
		calls := 0
		restart := func(string) error {
			calls++
			if calls == 1 || failAgain {
				return errors.New("service failed")
			}
			if installedVersion(svc) != "7.2.158-7ong.2" {
				t.Fatal("original executable must be restored before restart")
			}
			return nil
		}
		err := manageBackup(svc, false, bufio.NewScanner(strings.NewReader("y\n")), &output, restart)
		if err == nil || calls != 2 {
			t.Fatalf("error = %v, restart calls = %d", err, calls)
		}
		assertBackupVersions(t, svc, "7.2.158-7ong.2", "7.2.158-7ong.1")
	}
}

func TestBackupConfirmationAndLock(t *testing.T) {
	for _, remove := range []bool{false, true} {
		svc := backupFixture(t, "cpa-usage-keeper", "v1.15.4", "v1.15.3")
		var output strings.Builder
		restart := func(string) error { t.Fatal("unexpected restart"); return nil }
		for _, input := range []string{"n\n", ""} {
			if err := manageBackup(svc, remove, bufio.NewScanner(strings.NewReader(input)), &output, restart); err != nil {
				t.Fatal(err)
			}
			assertBackupVersions(t, svc, "v1.15.4", "v1.15.3")
		}
		dir, err := lockDirectory(svc.dir)
		if err != nil {
			t.Fatal(err)
		}
		err = manageBackup(svc, remove, bufio.NewScanner(strings.NewReader("y\n")), &output, restart)
		if closeErr := dir.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err == nil {
			t.Fatal("concurrent operation acquired lock")
		}
		assertBackupVersions(t, svc, "v1.15.4", "v1.15.3")
		if err := os.Remove(filepath.Join(svc.dir, svc.binary+".previous")); err != nil {
			t.Fatal(err)
		}
		output.Reset()
		if err := manageBackup(svc, remove, bufio.NewScanner(strings.NewReader("y\n")), &output, restart); err != nil || !strings.Contains(output.String(), "没有本地备份") {
			t.Fatalf("missing backup: %v, %s", err, &output)
		}
	}
}

func TestBackupRestoreCancellation(t *testing.T) {
	svc := backupFixture(t, "cpa-usage-keeper", "v1.15.4", "v1.15.3")
	dir, err := lockDirectory(svc.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = restoreBackup(ctx, svc, dir, func(string) error { t.Fatal("unexpected restart"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	assertBackupVersions(t, svc, "v1.15.4", "v1.15.3")
}

func TestBackupRestoreRecoveryCopy(t *testing.T) {
	svc := backupFixture(t, "cpa-usage-keeper", "v1.15.4", "v1.15.3")
	var output strings.Builder
	err := manageBackup(svc, false, bufio.NewScanner(strings.NewReader("y\n")), &output, func(string) error {
		// A directory at the target simulates a filesystem obstacle to rollback.
		target := filepath.Join(svc.dir, svc.binary)
		if err := os.Remove(target); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		return errors.New("restart failed")
	})
	saved := filepath.Join(svc.workDir(), "current")
	if err == nil || !strings.Contains(err.Error(), saved) {
		t.Fatalf("recovery path missing: %v", err)
	}
	if got := executableVersion(svc, saved); got != "v1.15.4" {
		t.Fatalf("original recovery copy = %s", got)
	}
	if got := executableVersion(svc, filepath.Join(svc.dir, svc.binary+".previous")); got != "v1.15.3" {
		t.Fatalf("original backup = %s", got)
	}
}
