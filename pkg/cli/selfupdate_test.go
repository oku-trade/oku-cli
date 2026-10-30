package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type tarEntry struct {
	name string
	body string
	mode int64
}

func TestManagedByDistribution(t *testing.T) {
	for _, tc := range []struct {
		path, manager string
	}{
		{"/usr/local/lib/node_modules/@gfxlabs/oku-cli-linux-x64/bin/oku", "npm"},
		{"/home/user/project/node_modules/@gfxlabs/oku-cli-darwin-arm64/bin/oku", "npm"},
		{"/home/user/go/bin/oku", ""},
		{"/home/user/.local/bin/oku", ""},
	} {
		got, _ := managedBy(tc.path)
		if got != tc.manager {
			t.Errorf("managedBy(%q) = %q, want %q", tc.path, got, tc.manager)
		}
	}
	if runtime.GOOS == "linux" {
		got, _ := managedBy("/usr/bin/oku")
		if got != "system package" {
			t.Errorf("managedBy(/usr/bin/oku) = %q", got)
		}
	}
}

func TestReleaseChecksumSelectsArchive(t *testing.T) {
	name := "oku_v1.2.3_linux_amd64.tar.gz"
	sums := []byte("aaa  oku_1.2.3_linux_amd64.deb\nbbb  oku-1.2.3-1.x86_64.rpm\nccc  " + name + "\n")
	if got := sumFor(sums, name); got != "ccc" {
		t.Fatalf("sumFor archive = %q, want ccc", got)
	}
	assets := ghRelease{Assets: []ghAsset{{Name: "oku_1.2.3_linux_amd64.deb"}, {Name: name}}}
	if asset := assets.asset(name); asset == nil || asset.Name != name {
		t.Fatalf("release asset selection did not choose %s", name)
	}
}

func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
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

type fakeRelease struct {
	tag     string
	archive []byte              // archive for this platform; nil means no asset
	sums    func([]byte) string // SHA256SUMS content
	token   bool
}

func (f *fakeRelease) server(t *testing.T) *httptest.Server {
	asset := fmt.Sprintf("oku_%s_%s_%s.tar.gz", f.tag, runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantAuth := ""
		if f.token {
			wantAuth = "Bearer test-token"
		}
		if got := r.Header.Get("Authorization"); got != wantAuth {
			t.Errorf("authorization header does not match optional token setting")
		}
		switch r.URL.Path {
		case "/repos/oku-trade/oku-cli/releases/latest", "/repos/oku-trade/oku-cli/releases/tags/" + f.tag:
			assets := []map[string]any{{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/assets/sums"}}
			if f.archive != nil {
				assets = append(assets, map[string]any{"name": asset, "browser_download_url": srv.URL + "/assets/archive"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": f.tag, "assets": assets})
		case "/assets/sums":
			fmt.Fprint(w, f.sums(f.archive))
		case "/assets/archive":
			_, _ = w.Write(f.archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func goodSums(tag string) func([]byte) string {
	return func(b []byte) string {
		s := sha256.Sum256(b)
		return fmt.Sprintf("%s  oku_%s_%s_%s.tar.gz\n", hex.EncodeToString(s[:]), tag, runtime.GOOS, runtime.GOARCH)
	}
}

// fakeBinary is a script that answers `version --json` like the real binary.
func fakeBinary(tag string) string {
	return "#!/bin/sh\necho '{\"ok\":true,\"data\":{\"version\":\"" + tag + "\"}}'\n"
}

func stem(tag string) string {
	return fmt.Sprintf("oku_%s_%s_%s", tag, runtime.GOOS, runtime.GOARCH)
}

type suRun struct {
	code int
	out  string
	env  map[string]any
}

func runSelfUpdate(t *testing.T, version, exe, apiURL string, args ...string) suRun {
	t.Helper()
	old := selfExecutable
	selfExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { selfExecutable = old })
	var out, errb bytes.Buffer
	a := &App{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader(""), Version: version,
		Getenv: func(k string) string {
			switch k {
			case "OKU_GITHUB_API_URL":
				return apiURL
			}
			return ""
		}}
	code := a.Run(append([]string{"self-update", "--json"}, args...))
	var env map[string]any
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("bad json %q: %v (stderr %s)", out.String(), err, errb.String())
	}
	return suRun{code, out.String(), env}
}

func errCode(r suRun) string {
	e, _ := r.env["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func installedExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("posix only")
	}
	exe := filepath.Join(t.TempDir(), "oku")
	if err := os.WriteFile(exe, []byte(fakeBinary("v0.0.1")), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestSelfUpdateReplacesBinary(t *testing.T) {
	exe := installedExe(t)
	f := &fakeRelease{tag: "v0.0.2", archive: makeTarGz(t, []tarEntry{{stem("v0.0.2") + "/README.md", "hi", 0o644}, {stem("v0.0.2") + "/oku", fakeBinary("v0.0.2"), 0o755}})}
	f.sums = goodSums(f.tag)
	srv := f.server(t)

	chk := runSelfUpdate(t, "v0.0.1", exe, srv.URL, "--check")
	data, _ := chk.env["data"].(map[string]any)
	if chk.code != 0 || data["latest"] != "v0.0.2" || data["update_available"] != true {
		t.Fatalf("check: %d %s", chk.code, chk.out)
	}

	r := runSelfUpdate(t, "v0.0.1", exe, srv.URL)
	if r.code != 0 {
		t.Fatalf("update failed: %s", r.out)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != fakeBinary("v0.0.2") {
		t.Fatalf("binary not replaced: %q", got)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", st.Mode())
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".oku-update-*"))
	if len(left) != 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestSelfUpdateAlreadyUpToDate(t *testing.T) {
	exe := installedExe(t)
	f := &fakeRelease{tag: "v0.0.1", sums: goodSums("v0.0.1")}
	r := runSelfUpdate(t, "v0.0.1", exe, f.server(t).URL)
	data, _ := r.env["data"].(map[string]any)
	if r.code != 0 || data["updated"] != false {
		t.Fatalf("%d %s", r.code, r.out)
	}
}

func TestSelfUpdateRefusals(t *testing.T) {
	exe := installedExe(t)
	tag := "v0.0.2"
	cases := []struct {
		name    string
		archive []byte
		sums    func([]byte) string
		code    string
	}{
		{"checksum mismatch", makeTarGz(t, []tarEntry{{stem(tag) + "/oku", fakeBinary(tag), 0o755}}), func([]byte) string {
			return strings.Repeat("0", 64) + "  " + stem(tag) + ".tar.gz\n"
		}, "REJECTED"},
		{"missing sums entry", makeTarGz(t, []tarEntry{{stem(tag) + "/oku", fakeBinary(tag), 0o755}}), func([]byte) string { return "" }, "REJECTED"},
		{"missing platform asset", nil, goodSums(tag), "NOT_FOUND"},
		{"tar traversal", makeTarGz(t, []tarEntry{{"../evil", "x", 0o755}, {stem(tag) + "/oku", fakeBinary(tag), 0o755}}), goodSums(tag), "UPSTREAM_ERROR"},
		{"wrong version inside", makeTarGz(t, []tarEntry{{stem(tag) + "/oku", fakeBinary("v9.9.9"), 0o755}}), goodSums(tag), "REJECTED"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeRelease{tag: tag, archive: c.archive, sums: c.sums}
			r := runSelfUpdate(t, "v0.0.1", exe, f.server(t).URL)
			if r.code == 0 || errCode(r) != c.code {
				t.Fatalf("want %s, got %d %s", c.code, r.code, r.out)
			}
			if got, _ := os.ReadFile(exe); string(got) != fakeBinary("v0.0.1") {
				t.Fatal("binary was modified")
			}
		})
	}
}

func TestSelfUpdateManagedAndDevRefusal(t *testing.T) {
	f := &fakeRelease{tag: "v0.0.2", sums: goodSums("v0.0.2")}
	srv := f.server(t)
	for _, exe := range []string{
		"/home/u/.local/share/mise/installs/github-oku-trade-oku-cli/0.0.1/oku",
		"/nix/store/abc-oku-0.0.1/bin/oku",
		"/opt/homebrew/Cellar/oku/0.0.1/bin/oku",
	} {
		r := runSelfUpdate(t, "v0.0.1", exe, srv.URL, "--force")
		if r.code == 0 || errCode(r) != "FILE_EXISTS" {
			t.Fatalf("%s: %d %s", exe, r.code, r.out)
		}
	}
	r := runSelfUpdate(t, "dev", installedExe(t), srv.URL)
	if r.code == 0 || !strings.Contains(r.out, "make install") {
		t.Fatalf("dev build: %d %s", r.code, r.out)
	}
}

func TestGitHubAPIBaseRestricted(t *testing.T) {
	for raw, ok := range map[string]bool{"": true, "https://ghe.example": true, "http://127.0.0.1:9": true, "http://evil.example": false} {
		a := &App{Getenv: func(string) string { return raw }}
		if _, err := a.githubAPIBase(); (err == nil) != ok {
			t.Errorf("%q: err=%v", raw, err)
		}
	}
}
