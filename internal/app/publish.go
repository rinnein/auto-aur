package app

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/rinnein/auto-aur/internal/aur"
	"github.com/rinnein/auto-aur/internal/fsx"
	"github.com/rinnein/auto-aur/internal/gitx"
	"github.com/rinnein/auto-aur/repo"
)

// AURPublisher publishes package repositories via git.
type AURPublisher struct {
	OutDir string
	Remote aur.Remote
	Name   string
	Email  string
	Log    func(format string, args ...any)
	// RPCBaseURL overrides the RPC endpoint; empty derives it from Remote.Host.
	// Used by tests that point the remote at a local directory.
	RPCBaseURL string
}

// rpcClient builds the RPC client for this publisher.
func (p *AURPublisher) rpcClient() *aur.Client {
	if p.RPCBaseURL != "" {
		c := aur.NewClient(p.Remote.Host)
		c.BaseURL = p.RPCBaseURL
		return c
	}
	return aur.NewClient(p.Remote.Host)
}

// Prepare checks out the package repository so the caller can read the
// previously published files (notably auto-aur.lock.json) before deciding what
// to render. Without this the lock would be unavailable on a fresh CI run and
// pkgrel could never be bumped for a rebuilt asset.
func (p *AURPublisher) Prepare(pkgbase string) error {
	logf := p.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	dir := filepath.Join(p.OutDir, pkgbase)
	if fsx.IsDir(filepath.Join(dir, ".git")) {
		return nil
	}

	runner := &gitx.Runner{Dir: dir, Name: p.Name, Email: p.Email}
	wd := &aur.Workdir{Dir: dir, Pkgbase: pkgbase, Remote: p.Remote, Git: runner}

	_, exists, err := p.rpcClient().Lookup(pkgbase)
	if err != nil {
		return err
	}
	if exists {
		logf("aur: %s exists", pkgbase)
	} else {
		logf("aur: %s does not exist yet; it will be created", pkgbase)
	}

	empty, err := wd.Clone()
	if err != nil {
		return err
	}
	if empty {
		logf("aur: %s has no published content yet", pkgbase)
	}
	return nil
}

// Publish writes the files into the prepared checkout and pushes them.
//
// The AUR treats a push to a pkgbase that does not exist yet as package
// creation, provided the SSH key is known to the account.
func (p *AURPublisher) Publish(req PublishRequest) (aur.Action, error) {
	logf := p.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	dir := filepath.Join(p.OutDir, req.Pkgbase)

	runner := &gitx.Runner{Dir: dir, Name: p.Name, Email: p.Email}
	wd := &aur.Workdir{Dir: dir, Pkgbase: req.Pkgbase, Remote: p.Remote, Git: runner}

	action, err := wd.Push(aur.Files{
		Pkgbuild: req.Files.Pkgbuild,
		Srcinfo:  req.Files.Srcinfo,
		Lock:     req.Files.Lock,
	})
	if err != nil {
		return action, err
	}
	if action == aur.ActionCreated || action == aur.ActionRecreated {
		// Confirm the repository is now visible; a lagging index is not an
		// error, but an absent package after a successful push is worth saying.
		if _, ok, lerr := p.rpcClient().Lookup(req.Pkgbase); lerr == nil && !ok {
			logf("aur: %s pushed but not yet listed by the RPC (index lag)", req.Pkgbase)
		}
	}
	return action, nil
}

// MakepkgSrcinfo regenerates .SRCINFO with makepkg when that tool is present,
// which keeps the file byte-identical to what a packager would produce locally.
func MakepkgSrcinfo(dir string) (string, bool) {
	if _, err := exec.LookPath("makepkg"); err != nil {
		return "", false
	}
	cmd := exec.Command("makepkg", "--printsrcinfo")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PKGDEST=/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	s := string(out)
	return s, strings.TrimSpace(s) != ""
}

// HTTPChecksum downloads a checksum file from a release asset URL.
//
// Release assets redirect to storage hosts, which must not receive the API
// token, so the Authorization header is only attached for the API host itself.
func HTTPChecksum(token string) repo.Checksum {
	client := &http.Client{Timeout: 30 * time.Second}
	return func(url string) (string, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		if token != "" && strings.HasPrefix(url, "https://api.github.com/") {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		// A checksum file is tiny; cap the read so a wrong URL cannot stream a
		// whole AppImage into memory.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if err != nil {
			return "", err
		}
		return string(body), nil
	}
}

// HTTPDigest streams an asset and returns its hex digest.
//
// Release assets live behind redirects to storage hosts, which must not receive
// the API token, so Authorization is only attached to the API host. The body is
// hashed while streaming: a 40 MB .deb is never held in memory.
func HTTPDigest(token string) repo.Digest {
	client := &http.Client{Timeout: 30 * time.Minute}
	return func(url, algo string) (string, error) {
		var h hash.Hash
		switch algo {
		case "sha256":
			h = sha256.New()
		case "sha1":
			h = sha1.New()
		default:
			return "", fmt.Errorf("unsupported digest %q", algo)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		if token != "" && strings.HasPrefix(url, "https://api.github.com/") {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		if _, err := io.Copy(h, resp.Body); err != nil {
			return "", fmt.Errorf("read %s: %w", url, err)
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
}
