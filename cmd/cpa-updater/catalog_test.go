package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestVersionSelection(t *testing.T) {
	first := []release{{Tag: "v3.0.0-rc1", Prerelease: true}, {Tag: "draft", Draft: true}, {Tag: "v2.0.0"}}
	for len(first) < pageSize {
		first = append(first, release{Tag: "v1.9.0"})
	}
	for _, tc := range []struct {
		name, input, want string
		pages             []string
		cancel            bool
	}{
		{"latest", "\n", "", []string{"1"}, false},
		{"stable-index", "1\n", "v2.0.0", []string{"1"}, false},
		{"next", "n\n1\n", "v1.0.0", []string{"1", "2"}, false},
		{"previous", "n\np\n1\n", "v2.0.0", []string{"1", "2", "1"}, false},
		{"first-boundary", "p\n1\n", "v2.0.0", []string{"1"}, false},
		{"last-boundary", "n\nn\n1\n", "v1.0.0", []string{"1", "2"}, false},
		{"invalid-index", "99\n1\n", "v2.0.0", []string{"1"}, false},
		{"exact", "v0.9.0\n", "v0.9.0", []string{"1"}, false},
		{"prefix", "0.9.0\n", "v0.9.0", []string{"1"}, false},
		{"cancel", "0\n", "", []string{"1"}, true},
		{"eof", "", "", []string{"1"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var out bytes.Buffer
			got, err := chooseVersion(bufio.NewScanner(strings.NewReader(tc.input)), &out, "owner/repo", "v1.9.0", func(url string) ([]byte, error) {
				if calls >= len(tc.pages) || !strings.HasSuffix(url, "per_page=10&page="+tc.pages[calls]) {
					t.Fatalf("unexpected request %s", url)
				}
				calls++
				if strings.HasSuffix(url, "page=1") {
					return json.Marshal(first)
				}
				return json.Marshal([]release{{Tag: "v1.0.0"}})
			})
			if got != tc.want || (tc.cancel && err != io.EOF) || (!tc.cancel && err != nil) || calls != len(tc.pages) {
				t.Fatalf("got %q, error %v, calls %d", got, err, calls)
			}
			if strings.Contains(out.String(), "rc1") || strings.Contains(out.String(), "draft") {
				t.Fatal("listed a draft or prerelease")
			}
		})
	}
}

func TestVersionListFailure(t *testing.T) {
	for _, fetch := range []func(string) ([]byte, error){
		func(string) ([]byte, error) { return nil, errors.New("offline") },
		func(string) ([]byte, error) { return []byte("invalid JSON"), nil },
	} {
		if _, err := chooseVersion(bufio.NewScanner(strings.NewReader("\n")), io.Discard, "owner/repo", "未知", fetch); err == nil {
			t.Fatal("accepted unavailable release list")
		}
	}
}

func TestReleaseURL(t *testing.T) {
	svc, err := selectService("official-cpa")
	if err != nil {
		t.Fatal(err)
	}
	if svc.releaseURL() != "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/latest" {
		t.Fatal(svc.releaseURL())
	}
	svc.tag = normalizeTag("7.2.158")
	if svc.releaseURL() != "https://api.github.com/repos/router-for-me/CLIProxyAPI/releases/tags/v7.2.158" {
		t.Fatal(svc.releaseURL())
	}
	svc.tag = "build/test"
	if !strings.HasSuffix(svc.releaseURL(), "tags/build%2Ftest") {
		t.Fatal(svc.releaseURL())
	}
}

func TestMenuExit(t *testing.T) {
	for _, input := range []string{"0\n", ""} {
		if err := run(nil, strings.NewReader(input), io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"--help"}, {"--version"}} {
		var out bytes.Buffer
		if err := run(args, strings.NewReader(""), &out); err != nil || out.Len() == 0 {
			t.Fatalf("%v: %v", args, err)
		}
	}
}
