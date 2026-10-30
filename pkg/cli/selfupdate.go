package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/oku-trade/oku-cli/pkg/output"
	urfave "github.com/urfave/cli/v3"
)

const (
	selfUpdateRepo        = "oku-trade/oku-cli"
	selfUpdateTimeout     = 2 * time.Minute
	selfUpdateMaxTimeout  = 10 * time.Minute
	selfUpdateMaxArchive  = 100 << 20
	selfUpdateMaxBinary   = 200 << 20
	selfUpdateManualInstr = "download the archive and SHA256SUMS from https://github.com/oku-trade/oku-cli/releases"
)

// selfExecutable resolves the running binary; tests replace it.
var selfExecutable = func() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func selfUpdateFlags() []urfave.Flag {
	return []urfave.Flag{
		&urfave.BoolFlag{Name: "check", Usage: "only report the current and latest version; download nothing"},
		&urfave.StringFlag{Name: "version", Usage: "install this release tag (vX.Y.Z) instead of the latest; allows downgrades"},
		&urfave.BoolFlag{Name: "force", Usage: "reinstall even if already on that version (does not override mise/nix/Homebrew refusal)"},
	}
}

type semver [3]int

func parseSemver(v string) (semver, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var s semver
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" {
			return semver{}, false
		}
		s[i] = n
	}
	return s, true
}

func (s semver) cmp(o semver) int {
	for i := range s {
		if s[i] != o[i] {
			if s[i] < o[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// managedBy reports which package manager owns exe, if any.
func managedBy(exe string) (string, string) {
	p := filepath.ToSlash(exe)
	switch {
	case strings.Contains(p, "/mise/installs/"):
		return "mise", "upgrade with: mise up github:oku-trade/oku-cli"
	case strings.Contains(p, "/nix/store/"):
		return "nix", "this binary is managed by Nix; upgrade through your flake or nix profile (nix profile upgrade oku)"
	case strings.Contains(p, "/Cellar/"):
		return "homebrew", "this binary is managed by Homebrew; upgrade with brew"
	}
	return "", ""
}

// githubAPIBase returns the API base. OKU_GITHUB_API_URL is for tests only and
// must be HTTPS or a loopback http URL.
func (a *App) githubAPIBase() (string, error) {
	raw := strings.TrimRight(a.Getenv("OKU_GITHUB_API_URL"), "/")
	if raw == "" {
		return "https://api.github.com", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", output.Errorf(output.CodeConfigError, "OKU_GITHUB_API_URL is not a valid URL")
	}
	if u.Scheme == "https" {
		return raw, nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return raw, nil
	}
	return "", output.Errorf(output.CodeConfigError, "OKU_GITHUB_API_URL must be https (or http on loopback, for tests)")
}

// githubToken reads an optional token. Public releases need no authentication.
func (a *App) githubToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if t := strings.TrimSpace(a.Getenv(k)); t != "" {
			return t
		}
	}
	return ""
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghClient struct {
	base, token string
	http        *http.Client
}

func (c *ghClient) get(ctx context.Context, u, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, output.Wrap(output.CodeUpstreamTimeout, err, "GitHub request")
		}
		return nil, output.Wrap(output.CodeUpstream, err, "GitHub request")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, output.Errorf(output.CodeNotFound, "GitHub returned 404 (no such public release for %s)", selfUpdateRepo)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, output.Errorf(output.CodeUpstream, "GitHub returned %d; check API rate limits or set GITHUB_TOKEN", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, output.Errorf(output.CodeUpstream, "GitHub returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "GitHub download")
	}
	if int64(len(body)) > limit {
		return nil, output.Errorf(output.CodeUpstream, "GitHub response exceeds %d bytes", limit)
	}
	return body, nil
}

func (c *ghClient) release(ctx context.Context, tag string) (*ghRelease, error) {
	u := c.base + "/repos/" + selfUpdateRepo + "/releases/latest"
	if tag != "" {
		u = c.base + "/repos/" + selfUpdateRepo + "/releases/tags/" + url.PathEscape(tag)
	}
	body, err := c.get(ctx, u, "application/vnd.github+json", 4<<20)
	if err != nil {
		return nil, err
	}
	var r ghRelease
	if err := json.Unmarshal(body, &r); err != nil || r.TagName == "" {
		return nil, output.Errorf(output.CodeUpstream, "unexpected GitHub release response")
	}
	if tag == "" && (r.Draft || r.Prerelease) {
		return nil, output.Errorf(output.CodeNotFound, "latest release %s is a draft or prerelease", r.TagName)
	}
	return &r, nil
}

func (r *ghRelease) asset(name string) *ghAsset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// sumFor finds name in a SHA256SUMS file.
func sumFor(sums []byte, name string) string {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 2 && (f[1] == name || f[1] == "*"+name) {
			return strings.ToLower(f[0])
		}
	}
	return ""
}

// extractBinary returns the contents of want from a .tar.gz, rejecting unsafe entries.
func extractBinary(archive []byte, want string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, output.Wrap(output.CodeUpstream, err, "release archive")
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, output.Errorf(output.CodeUpstream, "release archive does not contain %s", want)
		}
		if err != nil {
			return nil, output.Wrap(output.CodeUpstream, err, "release archive")
		}
		name := strings.TrimPrefix(h.Name, "./")
		if path.IsAbs(name) || strings.Contains(name, "\\") || path.Clean(name) != strings.TrimSuffix(name, "/") || strings.HasPrefix(path.Clean(name), "..") {
			return nil, output.Errorf(output.CodeUpstream, "release archive has unsafe entry %q", h.Name)
		}
		if name != want {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return nil, output.Errorf(output.CodeUpstream, "%s in release archive is not a regular file", want)
		}
		if h.Size <= 0 || h.Size > selfUpdateMaxBinary {
			return nil, output.Errorf(output.CodeUpstream, "%s in release archive has bad size %d", want, h.Size)
		}
		b, err := io.ReadAll(io.LimitReader(tr, selfUpdateMaxBinary+1))
		if err != nil {
			return nil, output.Wrap(output.CodeUpstream, err, "release archive")
		}
		if int64(len(b)) != h.Size {
			return nil, output.Errorf(output.CodeUpstream, "%s in release archive is truncated", want)
		}
		return b, nil
	}
}

func darwinTranslated() bool {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "amd64" {
		return false
	}
	out, err := exec.Command("sysctl", "-n", "sysctl.proc_translated").Output()
	return err == nil && strings.TrimSpace(string(out)) == "1"
}

func (a *App) cmdSelfUpdate(e *env) (*result, error) {
	ctx, cancel := context.WithTimeout(e.ctx, e.g.timeout)
	defer cancel()

	current := a.Version
	curSV, curOK := parseSemver(current)
	check := e.cmd.Bool("check")
	force := e.cmd.Bool("force")
	want := strings.TrimSpace(e.cmd.String("version"))
	if want != "" && !strings.HasPrefix(want, "v") {
		want = "v" + want
	}
	if want != "" {
		if _, ok := parseSemver(want); !ok {
			return nil, output.Errorf(output.CodeInvalidArgument, "--version %q is not a release tag like v1.2.3", want)
		}
	}

	exe, err := selfExecutable()
	if err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "resolve running executable")
	}
	manager, hint := managedBy(exe)
	if !check {
		if manager != "" {
			return nil, output.Errorf(output.CodeConflict, "%s is managed by %s; not replacing it", exe, manager).WithDetail("hint", hint).WithDetail("managed_by", manager)
		}
		if !curOK {
			return nil, output.Errorf(output.CodeConflict, "this is a development build (version %q); not replacing it", current).
				WithDetail("hint", "update the checkout instead: git pull && make install")
		}
	}

	base, err := a.githubAPIBase()
	if err != nil {
		return nil, err
	}
	gh := &ghClient{base: base, token: a.githubToken(), http: &http.Client{}}
	rel, err := gh.release(ctx, want)
	if err != nil {
		return nil, err
	}
	target := rel.TagName
	tgtSV, ok := parseSemver(target)
	if !ok || (want != "" && target != want) {
		return nil, output.Errorf(output.CodeUpstream, "unexpected release tag %q", target)
	}
	available := !curOK || curSV.cmp(tgtSV) < 0
	data := map[string]any{"current": current, "latest": target, "update_available": available, "path": exe}
	if want != "" {
		data["requested"] = want
	}
	if manager != "" {
		data["managed_by"] = manager
	}
	if check {
		return &result{data: data, human: func(w io.Writer) {
			fmt.Fprintf(w, "current: %s\nlatest:  %s\n", current, target)
			switch {
			case manager != "" && available:
				fmt.Fprintf(w, "update available (%s)\n", hint)
			case available:
				fmt.Fprintln(w, "update available: run 'oku self-update'")
			default:
				fmt.Fprintln(w, "already up to date")
			}
		}}, nil
	}

	c := curSV.cmp(tgtSV)
	if !force && (c == 0 || (c > 0 && want == "")) {
		data["updated"] = false
		return &result{data: data, human: func(w io.Writer) {
			fmt.Fprintf(w, "already up to date (%s)\n", current)
		}}, nil
	}

	goarch := runtime.GOARCH
	if darwinTranslated() {
		goarch = "arm64"
	}
	stem := fmt.Sprintf("oku_%s_%s_%s", target, runtime.GOOS, goarch)
	assetName := stem + ".tar.gz"
	asset := rel.asset(assetName)
	if asset == nil {
		return nil, output.Errorf(output.CodeNotFound, "release %s has no asset %s for this platform", target, assetName)
	}
	sumsAsset := rel.asset("SHA256SUMS")
	if sumsAsset == nil {
		return nil, output.Errorf(output.CodeNotFound, "release %s has no SHA256SUMS", target)
	}
	e.diag("downloading %s", assetName)
	sums, err := gh.get(ctx, sumsAsset.BrowserDownloadURL, "application/octet-stream", 1<<20)
	if err != nil {
		return nil, err
	}
	wantSum := sumFor(sums, assetName)
	if wantSum == "" {
		return nil, output.Errorf(output.CodeRejected, "%s is not listed in SHA256SUMS; refusing to install", assetName)
	}
	archive, err := gh.get(ctx, asset.BrowserDownloadURL, "application/octet-stream", selfUpdateMaxArchive)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(archive)
	if got := hex.EncodeToString(sum[:]); got != wantSum {
		return nil, output.Errorf(output.CodeRejected, "checksum mismatch for %s (want %s, got %s); refusing to install", assetName, wantSum, got)
	}
	bin, err := extractBinary(archive, stem+"/oku")
	if err != nil {
		return nil, err
	}

	dir := filepath.Dir(exe)
	mode := os.FileMode(0o755)
	if st, err := os.Stat(exe); err == nil {
		mode = st.Mode().Perm() | 0o500
	}
	tmp, err := os.CreateTemp(dir, ".oku-update-*")
	if err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "cannot write to "+dir).
			WithDetail("hint", "reinstall manually (no sudo needed for ~/.local/bin): "+selfUpdateManualInstr)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return nil, output.Wrap(output.CodeInternal, err, "write new binary")
	}
	if err := tmp.Close(); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "write new binary")
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return nil, output.Wrap(output.CodeInternal, err, "chmod new binary")
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("xattr", "-d", "com.apple.quarantine", tmpName).Run()
	}
	if err := verifyNewBinary(ctx, tmpName, target); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, exe); err != nil {
		return nil, output.Wrap(output.CodeConfigError, err, "replace "+exe).
			WithDetail("hint", "reinstall manually: "+selfUpdateManualInstr)
	}
	data["updated"] = true
	data["previous"] = current
	data["version"] = target
	return &result{data: data, human: func(w io.Writer) {
		fmt.Fprintf(w, "updated %s: %s -> %s\n", exe, current, target)
	}}, nil
}

// verifyNewBinary runs `<bin> version --json` and checks the reported tag.
func verifyNewBinary(ctx context.Context, bin, tag string) error {
	vctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(vctx, bin, "version", "--json").Output()
	if err != nil {
		return output.Wrap(output.CodeRejected, err, "downloaded binary failed to run; not installed")
	}
	var env struct {
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &env); err != nil || env.Data.Version != tag {
		return output.Errorf(output.CodeRejected, "downloaded binary reports version %q, want %s; not installed", env.Data.Version, tag)
	}
	return nil
}
