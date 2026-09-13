package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

var version = "dev"
var updaterRepo = "7ongOrz/cpa-updater"

const usage = `用法: cpa-updater [目标 [版本]]

直接更新最新版:
  cpa-updater cli-proxy-api       自维护 CPA
  cpa-updater official-cpa        官方 CPA
  cpa-updater cpa-usage-keeper     统计服务
  cpa-updater self                更新器自身

指定版本:
  cpa-updater official-cpa v7.2.158
  cpa-updater cpa-usage-keeper v1.15.4

无参数进入交互菜单；--version 查看版本。`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "操作失败:", err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			fmt.Fprintln(output, "cpa-updater", version)
			return nil
		case "--help", "-h":
			fmt.Fprintln(output, usage)
			return nil
		}
	}
	if len(args) > 2 {
		return fmt.Errorf("%s", usage)
	}
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return fmt.Errorf("支持 Linux amd64 和 arm64，当前为 %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	scanner := bufio.NewScanner(input)
	interactive := len(args) == 0
	choice := ""
	if interactive {
		fmt.Fprintf(output, "CPA Updater %s (%s)\n1. 更新自维护 CPA\n2. 更新 CPA Usage Keeper\n3. 更新官方 CPA\n4. 更新 updater 自身\n5. 恢复本地备份\n6. 删除本地备份\n0. 退出\n请选择: ", version, runtime.GOARCH)
		if !scanner.Scan() {
			return scanner.Err()
		}
		choice = strings.TrimSpace(scanner.Text())
	} else {
		choice = args[0]
	}
	if choice == "0" {
		return nil
	}
	if interactive && (choice == "5" || choice == "6") {
		return backupMenu(choice == "6", scanner, output)
	}
	svc, err := selectService(choice)
	if err != nil {
		return err
	}
	if !svc.self && os.Geteuid() != 0 {
		return fmt.Errorf("更新系统服务需要 root 权限")
	}
	history := svc.repo == "router-for-me/CLIProxyAPI" || svc.repo == "Willxup/cpa-usage-keeper"
	if len(args) == 2 {
		if !history {
			return fmt.Errorf("自维护 CPA 和更新器自身使用最新版；官方 CPA 和 Usage Keeper 支持指定版本")
		}
		svc.tag = normalizeTag(args[1])
	}
	current := installedVersion(svc)
	if interactive {
		fmt.Fprintf(output, "当前版本: %s\n", current)
		if history {
			svc.tag, err = chooseVersion(scanner, output, svc.repo, current, fetchJSON)
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	body, err := fetchJSON(svc.releaseURL())
	if err != nil {
		return err
	}
	var rel release
	if err = json.Unmarshal(body, &rel); err != nil {
		return fmt.Errorf("解析 release 信息: %w", err)
	}
	fmt.Fprintf(output, "\n来源: %s\n架构: linux/%s\n版本: %s → %s [%s]\n目录: %s\n", svc.repo, runtime.GOARCH, current, rel.Tag, versionChange(current, rel.Tag), svc.dir)
	if interactive {
		if !svc.self {
			fmt.Fprintln(output, "更新将重启服务并中断活动请求；降级前请确认配置和数据兼容。")
		}
		fmt.Fprint(output, "确认更新？[y/N]: ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		if answer := strings.TrimSpace(scanner.Text()); !strings.EqualFold(answer, "y") {
			return nil
		}
	}
	ctx, stop := installationContext()
	defer stop()
	if err = update(ctx, svc, rel, wgetDownload, restartService); err != nil {
		return err
	}
	if svc.self {
		fmt.Fprintln(output, "更新器已原位替换，下次启动使用新版；临时文件已清理。")
	} else {
		fmt.Fprintln(output, "更新完成，服务已启动，临时文件已清理。")
	}
	return nil
}

func installationContext() (context.Context, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// Keep a closed SSH output pipe from interrupting cleanup or installation.
	signal.Ignore(syscall.SIGPIPE)
	return ctx, func() {
		stop()
		signal.Reset(syscall.SIGPIPE)
	}
}
