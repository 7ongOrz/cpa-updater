package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const pageSize = 10

// Menu requests stay in memory so exiting the menu leaves no temporary files.
func fetchJSON(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wget", "--no-hsts", "--timeout=15", "--tries=2", "-qO-", url)
	cmd.Stderr = os.Stderr
	body, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("读取 GitHub 版本列表失败，请检查网络或稍后重试: %w", err)
	}
	return body, nil
}

func normalizeTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if len(tag) > 0 && tag[0] >= '0' && tag[0] <= '9' {
		return "v" + tag
	}
	return tag
}

func chooseVersion(scanner *bufio.Scanner, output io.Writer, repo, current string, fetch func(string) ([]byte, error)) (string, error) {
	page := 1
pages:
	for {
		body, err := fetch(fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d&page=%d", repo, pageSize, page))
		if err != nil {
			return "", err
		}
		var releases []release
		if err = json.Unmarshal(body, &releases); err != nil {
			return "", fmt.Errorf("解析版本列表: %w", err)
		}
		more := len(releases) == pageSize
		stable := make([]release, 0, len(releases))
		for _, rel := range releases {
			if rel.Draft || rel.Prerelease {
				continue
			}
			stable = append(stable, rel)
		}
		fmt.Fprintf(output, "\n%s · 第 %d 页\n", repo, page)
		for i, rel := range stable {
			fmt.Fprintf(output, "%2d. %s [%s]\n", i+1, rel.Tag, versionChange(current, rel.Tag))
		}
		if len(stable) == 0 {
			fmt.Fprintln(output, "本页没有稳定版本。")
		}
		fmt.Fprintln(output, "回车: 最新版 | 数字: 选择 | n/p: 翻页 | 完整版本号: 直接指定 | 0: 取消")
		for {
			fmt.Fprint(output, "版本: ")
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return "", err
				}
				return "", io.EOF
			}
			choice := strings.TrimSpace(scanner.Text())
			switch strings.ToLower(choice) {
			case "":
				return "", nil
			case "0":
				return "", io.EOF
			case "n":
				if more {
					page++
					continue pages
				}
				fmt.Fprintln(output, "已到最后一页。")
			case "p":
				if page > 1 {
					page--
					continue pages
				}
				fmt.Fprintln(output, "当前是第一页。")
			default:
				if index, err := strconv.Atoi(choice); err == nil {
					if index >= 1 && index <= len(stable) {
						return stable[index-1].Tag, nil
					}
					fmt.Fprintln(output, "请选择本页列出的编号，或输入完整版本号。")
					continue
				}
				return normalizeTag(choice), nil
			}
		}
	}
}
