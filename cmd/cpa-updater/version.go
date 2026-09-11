package main

import (
	"cmp"
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func installedVersion(svc service) string {
	if svc.self {
		return version
	}
	args := []string{"--version"}
	if svc.binary == "cli-proxy-api" {
		// CPA prints its version before help. An empty config keeps plugin loading out of this query.
		args = []string{"-config", "/dev/null", "-h"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, filepath.Join(svc.dir, svc.binary), args...).Output()
	if err != nil {
		return "未知"
	}
	return parseVersionOutput(svc.binary, string(out))
}

func parseVersionOutput(binary, output string) string {
	if binary == "cli-proxy-api" {
		for _, line := range strings.Split(output, "\n") {
			if rest, ok := strings.CutPrefix(line, "CLIProxyAPI Version: "); ok {
				value, _, _ := strings.Cut(rest, ",")
				return strings.TrimSpace(value)
			}
		}
	} else if fields := strings.Fields(output); len(fields) == 1 {
		return fields[0]
	}
	return "未知"
}

// Compare the stable version formats published by the three supported repositories.
func versionParts(tag string) (parts [4]uint64, fork bool, ok bool) {
	base, suffix, hasSuffix := strings.Cut(strings.TrimPrefix(tag, "v"), "-")
	fields := strings.Split(base, ".")
	if len(fields) != 3 {
		return parts, false, false
	}
	if hasSuffix {
		revision, matched := strings.CutPrefix(suffix, "7ong.")
		if !matched {
			return parts, false, false
		}
		fields = append(fields, revision)
		fork = true
	}
	for i, field := range fields {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return parts, fork, false
		}
		parts[i] = n
	}
	return parts, fork, true
}

func versionChange(current, target string) string {
	from, fromFork, fromOK := versionParts(current)
	to, toFork, toOK := versionParts(target)
	if !fromOK || !toOK {
		return "版本切换，顺序未知"
	}
	if fromFork != toFork {
		return "切换官方/自维护来源"
	}
	for i := range from {
		switch cmp.Compare(to[i], from[i]) {
		case 1:
			return "升级"
		case -1:
			return "降级"
		}
	}
	return "同版本重装"
}
