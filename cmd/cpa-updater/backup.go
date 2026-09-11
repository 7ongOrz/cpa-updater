package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func backupMenu(remove bool, scanner *bufio.Scanner, output io.Writer) error {
	fmt.Fprint(output, "\n1. CPA（官方/自维护共用）\n2. CPA Usage Keeper\n0. 取消\n请选择服务: ")
	if !scanner.Scan() {
		return scanner.Err()
	}
	var name string
	switch strings.TrimSpace(scanner.Text()) {
	case "0":
		return nil
	case "1":
		name = "cli-proxy-api"
	case "2":
		name = "cpa-usage-keeper"
	default:
		return fmt.Errorf("请选择 1、2 或 0")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("管理服务备份需要 root 权限")
	}
	svc, err := selectService(name)
	if err != nil {
		return err
	}
	return manageBackup(svc, remove, scanner, output, restartService)
}

func backupRelation(current, backup string) string {
	switch change := versionChange(current, backup); change {
	case "升级":
		return "比当前新，恢复将升级"
	case "降级":
		return "比当前旧，恢复将降级"
	case "同版本重装":
		return "与当前相同"
	default:
		return change
	}
}

func manageBackup(svc service, remove bool, scanner *bufio.Scanner, output io.Writer, restart restartFunc) (err error) {
	// Hold the same lock as online updates through preview, confirmation, and mutation.
	dir, err := lockDirectory(svc.dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dir.Close()) }()
	previous := filepath.Join(svc.dir, svc.binary+".previous")
	if _, err = os.Stat(previous); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(output, "该服务没有本地备份。")
		return nil
	} else if err != nil {
		return err
	}
	current, backup := installedVersion(svc), executableVersion(svc, previous)
	fmt.Fprintf(output, "\n当前版本: %s\n备份版本: %s [%s]\n备份路径: %s\n", current, backup, backupRelation(current, backup), previous)
	if remove {
		fmt.Fprint(output, "仅删除这份程序备份，当前程序和服务保持原样。\n确认删除？[y/N]: ")
	} else {
		fmt.Fprint(output, "恢复后重启服务，当前程序将成为新的备份。配置和数据库保持原样，请确认版本兼容。\n确认恢复？[y/N]: ")
	}
	if !scanner.Scan() {
		return scanner.Err()
	}
	if !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
		return nil
	}
	ctx, stop := installationContext()
	defer stop()
	if remove {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.Remove(previous); err != nil {
			return err
		}
		if err = dir.Sync(); err != nil {
			return err
		}
		fmt.Fprintln(output, "本地备份已删除，当前服务保持原样。")
		return nil
	}
	if err = restoreBackup(ctx, svc, dir, restart); err != nil {
		return err
	}
	fmt.Fprintln(output, "本地备份已恢复，服务已启动；原程序已保存为新的备份。")
	return nil
}

func restoreBackup(ctx context.Context, svc service, dir *os.File, restart restartFunc) (err error) {
	work := svc.workDir()
	if err = os.RemoveAll(work); err != nil {
		return err
	}
	if err = os.Mkdir(work, 0700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			err = errors.Join(err, os.RemoveAll(work))
		}
	}()
	target := filepath.Join(svc.dir, svc.binary)
	previous := target + ".previous"
	saved := filepath.Join(work, "current")
	staged := filepath.Join(work, "restored")
	if err = os.Link(target, saved); err != nil {
		return err
	}
	if err = os.Link(previous, staged); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Rename(staged, target); err != nil {
		return err
	}
	err = dir.Sync()
	if err == nil {
		err = restart(svc.binary)
	}
	if err == nil {
		// Commit the backup rotation after the restored service starts successfully.
		err = os.Rename(saved, previous)
	}
	if err != nil {
		if restoreErr := os.Rename(saved, target); restoreErr != nil {
			cleanup = false
			return errors.Join(err, fmt.Errorf("恢复原程序失败，原程序保留在 %s: %w", saved, restoreErr))
		}
		syncErr := dir.Sync()
		restartErr := restart(svc.binary)
		return errors.Join(fmt.Errorf("备份恢复失败，原程序已还原: %w", err), syncErr, restartErr)
	}
	return dir.Sync()
}
