package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionChange(t *testing.T) {
	for _, tc := range []struct{ current, target, want string }{
		{"v1.9.0", "v1.10.0", "升级"},
		{"1.15.4", "v1.15.3", "降级"},
		{"7.2.158", "v7.2.158", "同版本重装"},
		{"7.2.158-7ong.1", "v7.2.158-7ong.2", "升级"},
		{"7.2.158-7ong.2", "v7.2.158-7ong.1", "降级"},
		{"7.2.158-7ong.9", "v7.2.159-7ong.1", "升级"},
		{"7.2.158-7ong.1", "v7.2.158", "切换官方/自维护来源"},
		{"7.2.158", "v7.2.158-7ong.1", "切换官方/自维护来源"},
		{"dev", "v1.0.0", "版本切换，顺序未知"},
		{"未知", "v1.0.0", "版本切换，顺序未知"},
		{"v1.0.0", "v1.1.0-rc1", "版本切换，顺序未知"},
		{"v1.0.0", "v1.0.invalid", "版本切换，顺序未知"},
	} {
		if got := versionChange(tc.current, tc.target); got != tc.want {
			t.Errorf("%s -> %s: got %s, want %s", tc.current, tc.target, got, tc.want)
		}
	}
}

func TestVersionOutput(t *testing.T) {
	for _, tc := range []struct{ binary, output, want string }{
		{"cli-proxy-api", "CLIProxyAPI Version: 7.2.158-7ong.1, Commit: abc, BuiltAt: now\n", "7.2.158-7ong.1"},
		{"cli-proxy-api", "CLIProxyAPI Version: 7.2.158, Commit: abc\n", "7.2.158"},
		{"cpa-usage-keeper", "v1.15.4\n", "v1.15.4"},
		{"cpa-usage-keeper", "flag provided but not defined: -version", "未知"},
		{"cli-proxy-api", "", "未知"},
	} {
		if got := parseVersionOutput(tc.binary, tc.output); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.binary, got, tc.want)
		}
	}
}

func TestInstalledVersion(t *testing.T) {
	for _, tc := range []struct{ binary, args, output, want string }{
		{"cli-proxy-api", "-config /dev/null -h", "CLIProxyAPI Version: 7.2.158, Commit: abc", "7.2.158"},
		{"cpa-usage-keeper", "--version", "v1.15.4", "v1.15.4"},
	} {
		svc := service{binary: tc.binary, dir: t.TempDir()}
		path := filepath.Join(svc.dir, svc.binary)
		script := "#!/bin/sh\n[ \"$*\" = '" + tc.args + "' ] || exit 9\nprintf '%s\\n' '" + tc.output + "'\n"
		if err := os.WriteFile(path, []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
		if got := installedVersion(svc); got != tc.want {
			t.Fatalf("%s: got %s", tc.binary, got)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 2\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if got := installedVersion(svc); got != "未知" {
			t.Fatalf("unavailable version: %s", got)
		}
	}
}
