package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

type service struct {
	binary string
	repo   string
	dir    string
	tag    string
	self   bool
}

func selectService(choice string) (service, error) {
	switch choice {
	case "1", "cli-proxy-api":
		return service{binary: "cli-proxy-api", repo: "7ongOrz/CLIProxyAPI", dir: "/etc/cli-proxy-api"}, nil
	case "2", "cpa-usage-keeper":
		return service{binary: "cpa-usage-keeper", repo: "Willxup/cpa-usage-keeper", dir: "/etc/cpa-usage-keeper"}, nil
	case "3", "official-cpa":
		return service{binary: "cli-proxy-api", repo: "router-for-me/CLIProxyAPI", dir: "/etc/cli-proxy-api"}, nil
	case "4", "self":
		path, err := os.Executable()
		if err != nil {
			return service{}, err
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return service{}, err
		}
		return service{binary: filepath.Base(path), repo: updaterRepo, dir: filepath.Dir(path), self: true}, nil
	default:
		return service{}, fmt.Errorf("请选择菜单中的选项，或指定有效的更新目标名称")
	}
}

type release struct {
	Tag        string         `json:"tag_name"`
	Prerelease bool           `json:"prerelease"`
	Draft      bool           `json:"draft"`
	Assets     []releaseAsset `json:"assets"`
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

func (r release) assetURL(name string) (string, error) {
	for _, asset := range r.Assets {
		if asset.Name == name && asset.URL != "" {
			return asset.URL, nil
		}
	}
	return "", fmt.Errorf("release %s 尚未提供 %s；请等待发布完成", r.Tag, name)
}

func (s service) archive(tag, arch string) (name, member string) {
	if s.self {
		return "cpa-updater_linux_" + arch + ".tar.gz", "cpa-updater"
	}
	if s.binary == "cli-proxy-api" {
		if arch == "arm64" {
			arch = "aarch64"
		}
		return "CLIProxyAPI_" + strings.TrimPrefix(tag, "v") + "_linux_" + arch + ".tar.gz", s.binary
	}
	base := "cpa-usage-keeper_" + tag + "_linux_" + arch
	return base + ".tar.gz", base + "/" + s.binary
}

func (s service) releaseURL() string {
	base := "https://api.github.com/repos/" + s.repo + "/releases/"
	if s.tag != "" {
		return base + "tags/" + url.PathEscape(s.tag)
	}
	return base + "latest"
}

func (s service) workDir() string {
	if s.self {
		return filepath.Join(s.dir, s.binary+".update-tmp")
	}
	return filepath.Join(s.dir, "update-tmp")
}

type downloadFunc func(context.Context, string, string) error
type restartFunc func(string) error

// Lock the installation directory itself so cleanup never removes a lock inode.
func lockDirectory(path string) (*os.File, error) {
	dir, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = dir.Close()
		return nil, fmt.Errorf("该服务已有更新任务，或目录无法加锁: %w", err)
	}
	return dir, nil
}

func update(ctx context.Context, svc service, rel release, download downloadFunc, restart restartFunc) (err error) {
	dir, err := lockDirectory(svc.dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()

	work := svc.workDir()
	if err = os.RemoveAll(work); err != nil {
		return fmt.Errorf("清理历史临时文件: %w", err)
	}
	if err = os.Mkdir(work, 0700); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(work)) }()

	name, member := svc.archive(rel.Tag, runtime.GOARCH)
	archiveURL, err := rel.assetURL(name)
	if err != nil {
		return err
	}
	checksumsURL, err := rel.assetURL("checksums.txt")
	if err != nil {
		return err
	}
	fmt.Printf("更新 %s → %s\n", svc.binary, rel.Tag)
	archive := filepath.Join(work, "release.tar.gz")
	if err = download(ctx, archiveURL, archive); err != nil {
		return err
	}
	checksums := filepath.Join(work, "checksums.txt")
	if err = download(ctx, checksumsURL, checksums); err != nil {
		return err
	}
	if err = verifyChecksum(archive, checksums, name); err != nil {
		return err
	}
	staged := filepath.Join(work, svc.binary)
	if err = extractBinary(archive, member, staged); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}

	// Preserve one previous executable; configuration and databases stay in place.
	target := filepath.Join(svc.dir, svc.binary)
	previous := target + ".previous"
	if !svc.self {
		backup := filepath.Join(work, "previous")
		if err = os.Link(target, backup); err != nil {
			return fmt.Errorf("备份当前程序: %w", err)
		}
		if err = os.Rename(backup, previous); err != nil {
			return err
		}
		if err = dir.Sync(); err != nil {
			return err
		}
	}
	if err = os.Rename(staged, target); err != nil {
		return err
	}
	if err = dir.Sync(); err != nil {
		return fmt.Errorf("新程序已替换，目录同步失败，请检查服务: %w", err)
	}
	if svc.self {
		return nil
	}

	// Finish the short commit phase even when the SSH terminal disconnects.
	if err = restart(svc.binary); err != nil {
		if rollbackErr := os.Rename(previous, target); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("恢复旧程序失败，备份位于 %s: %w", previous, rollbackErr))
		}
		if syncErr := dir.Sync(); syncErr != nil {
			return errors.Join(err, syncErr)
		}
		if restartErr := restart(svc.binary); restartErr != nil {
			return errors.Join(err, fmt.Errorf("旧程序已恢复，服务启动失败: %w", restartErr))
		}
		return fmt.Errorf("新版本启动失败，已恢复并启动旧程序: %w", err)
	}
	return nil
}

func wgetDownload(ctx context.Context, url, destination string) error {
	cmd := exec.CommandContext(ctx, "wget", "--no-hsts", "--timeout=30", "--tries=3", "-O", destination, url)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wget 下载失败: %w", err)
	}
	return nil
}

func restartService(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, args := range [][]string{{"restart", name + ".service"}, {"is-active", "--quiet", name + ".service"}} {
		cmd := exec.CommandContext(ctx, "systemctl", args...)
		// Complete service control independently of terminal process-group signals.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %s: %w %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func verifyChecksum(archive, checksums, name string) error {
	list, err := os.Open(checksums)
	if err != nil {
		return err
	}
	defer list.Close()
	scanner := bufio.NewScanner(list)
	want := ""
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			want = fields[0]
			break
		}
	}
	if err = scanner.Err(); err != nil {
		return err
	}
	if want == "" {
		return fmt.Errorf("checksums.txt 尚未包含 %s", name)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return err
	}
	if !strings.EqualFold(want, hex.EncodeToString(hash.Sum(nil))) {
		return fmt.Errorf("%s SHA256 校验失败，当前程序保持原样", name)
	}
	return nil
}

func extractBinary(archive, member, destination string) (err error) {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		header, readErr := reader.Next()
		if readErr == io.EOF {
			return fmt.Errorf("压缩包中缺少 %s", member)
		}
		if readErr != nil {
			return readErr
		}
		if header.Name != member {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size == 0 {
			return fmt.Errorf("%s 应为非空的常规文件", member)
		}
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, reader)
		return errors.Join(copyErr, out.Chmod(0755), out.Sync(), out.Close())
	}
}
