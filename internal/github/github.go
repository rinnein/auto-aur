// Package github reads release metadata for the upstream repository that a
// task packages.
package github

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Release mirrors the fields auto-aur consumes from the GitHub REST API.
type Release struct {
	TagName     string  `json:"tag_name"`
	Name        string  `json:"name"`
	PublishedAt string  `json:"published_at"`
	Body        string  `json:"body"`
	Assets      []Asset `json:"assets"`
	Message     string  `json:"message"` // populated by API error responses
}

// Version extracts the upstream version a release represents.
//
// A rolling tag (the same tag rebuilt for every commit) cannot carry the
// version, so upstream records it in the release name and body instead:
//
//	name: "canary v0.10.0"   body: "版本：0.10.0"
//
// Both are checked, then the tag itself for repositories that tag per version.
// Assets are deliberately not consulted: a filename can go stale (the .deb in
// this very repository once said 0.10.0 while its control file said 0.9.9).
func (r *Release) Version() (string, error) {
	candidates := []string{}
	if v := versionFromName(r.Name); v != "" {
		candidates = append(candidates, v)
	}
	if v := versionFromBody(r.Body); v != "" {
		candidates = append(candidates, v)
	}
	if v := versionFromTag(r.TagName); v != "" {
		candidates = append(candidates, v)
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("github: cannot determine a version from release %q (name %q)", r.TagName, r.Name)
	}
	return candidates[0], nil
}

var semanticVersion = regexp.MustCompile(`\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.]+)?`)

// versionFromName pulls the version out of a release name such as
// "canary v0.10.0" or "v1.2.3".
func versionFromName(name string) string {
	return semanticVersion.FindString(name)
}

// versionFromBody looks for an explicit "版本：X" / "version: X" line.
func versionFromBody(body string) string {
	for _, line := range strings.Split(body, "\n") {
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "版本") && !strings.Contains(lower, "version") {
			continue
		}
		if v := semanticVersion.FindString(line); v != "" {
			return v
		}
	}
	return ""
}

// versionFromTag accepts tags that are themselves a version, optionally v
// prefixed. Rolling tags such as "canary" yield "".
func versionFromTag(tag string) string {
	trimmed := strings.TrimPrefix(tag, "v")
	if semanticVersion.FindString(trimmed) == trimmed && trimmed != "" {
		return trimmed
	}
	return ""
}

// Asset is a downloadable release artefact.
type Asset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
	UpdatedAt          string `json:"updated_at"`
	// Digest is "sha256:<hex>" as computed by GitHub when the asset was
	// uploaded. It lets the package checksum be read at run time instead of
	// downloading the artefact; older assets may leave it empty.
	Digest string `json:"digest"`
	State  string `json:"state"`
}

// SHA256 returns the sha256 from Digest, or "" when absent.
func (a Asset) SHA256() string {
	const prefix = "sha256:"
	if strings.HasPrefix(a.Digest, prefix) {
		sum := strings.TrimPrefix(a.Digest, prefix)
		if len(sum) == 64 {
			return sum
		}
	}
	return ""
}

// Repository mirrors the fields of GET /repos/{owner}/{repo} that auto-aur
// uses. Description is the "About" blurb shown on the repository page.
type Repository struct {
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Homepage    string `json:"homepage"`
	Message     string `json:"message"`
}

// RepoInfo reads repository metadata, notably the About description.
func (c *Client) RepoInfo(repo string) (*Repository, error) {
	body, err := c.fetchRaw("/repos/" + repo)
	if err != nil {
		return nil, err
	}
	var r Repository
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("github: decode repo %s: %w", repo, err)
	}
	return &r, nil
}

// FileVersion reads package.json at ref and returns its "version" field.
//
// VCS packages build from a branch, so there is no release to name a version;
// the branch's own package.json is the authority, and the PKGBUILD pkgver()
// reads the same file so the two cannot drift.
func (c *Client) FileVersion(repo, ref, path string) (string, error) {
	body, err := c.fetchRaw(fmt.Sprintf("/repos/%s/contents/%s?ref=%s", repo, path, ref))
	if err != nil {
		return "", err
	}
	var file struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return "", fmt.Errorf("github: decode %s@%s: %w", path, ref, err)
	}
	raw := file.Content
	if file.Encoding == "base64" {
		decoded, derr := base64.StdEncoding.DecodeString(strings.ReplaceAll(raw, "\n", ""))
		if derr != nil {
			return "", fmt.Errorf("github: decode %s@%s content: %w", path, ref, derr)
		}
		raw = string(decoded)
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
		return "", fmt.Errorf("github: parse %s@%s: %w", path, ref, err)
	}
	if manifest.Version == "" {
		return "", fmt.Errorf("github: %s@%s has no version field", path, ref)
	}
	return manifest.Version, nil
}

// Commit identifies a branch head.
type Commit struct {
	SHA  string `json:"sha"`
	Info struct {
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// Client talks to api.github.com. Token is optional but strongly recommended:
// unauthenticated requests are rate limited to 60/hour per IP, which a shared
// CI runner will exhaust quickly.
type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
	// Pages caps how many release pages are scanned in search mode.
	Pages int
}

// New builds a client with sane defaults.
func New(token string) *Client {
	return &Client{
		Token:   token,
		BaseURL: "https://api.github.com",
		HTTP:    &http.Client{Timeout: 30 * time.Second},
		Pages:   10,
	}
}

// Wanted selects the release described by spec.
type Wanted struct {
	Repo   string // owner/name
	Tag    string // explicit tag, takes precedence
	Search string // substring matched against release name, then tag
}

// ErrRateLimited reports an exhausted API quota.
var ErrRateLimited = fmt.Errorf("github: API rate limit exceeded; set GH_TOKEN or GITHUB_TOKEN")

// BranchHead returns the head commit of branch. VCS packages use it instead of
// a release, and its SHA doubles as the change fingerprint.
func (c *Client) BranchHead(repo, branch string) (*Commit, error) {
	body, err := c.fetchRaw(fmt.Sprintf("/repos/%s/commits/%s", repo, branch))
	if err != nil {
		return nil, err
	}
	var commit Commit
	if err := json.Unmarshal(body, &commit); err != nil {
		return nil, fmt.Errorf("github: decode commit %s@%s: %w", repo, branch, err)
	}
	if commit.SHA == "" {
		return nil, fmt.Errorf("github: no commit returned for %s@%s", repo, branch)
	}
	return &commit, nil
}

// Fetch resolves spec to a release and normalises it: substitutions are left to
// the caller so this package stays free of packaging policy.
func (c *Client) Fetch(spec Wanted) (*Release, error) {
	switch {
	case spec.Tag != "":
		return c.get(fmt.Sprintf("/repos/%s/releases/tags/%s", spec.Repo, spec.Tag))
	case spec.Search != "":
		return c.search(spec.Repo, spec.Search)
	default:
		return c.get(fmt.Sprintf("/repos/%s/releases/latest", spec.Repo))
	}
}

func (c *Client) search(repo, needle string) (*Release, error) {
	var all []Release
	for page := 1; page <= c.Pages; page++ {
		body, err := c.fetchRaw(fmt.Sprintf("/repos/%s/releases?per_page=100&page=%d", repo, page))
		if err != nil {
			return nil, err
		}
		var page1 []Release
		if err := json.Unmarshal(body, &page1); err != nil {
			return nil, fmt.Errorf("github: decode releases page %d: %w", page, err)
		}
		if len(page1) == 0 {
			break
		}
		all = append(all, page1...)
		if len(page1) < 100 {
			break
		}
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("github: %s has no releases", repo)
	}

	// Upstream does not always carry the channel in the release *name*
	// (e.g. a stable release is literally "6.4.0"), so matching the tag as a
	// fallback keeps pre-release discovery working.
	picked := filterBy(all, func(r Release) bool {
		return strings.Contains(r.Name, needle)
	})
	if len(picked) == 0 {
		picked = filterBy(all, func(r Release) bool {
			return strings.Contains(r.TagName, needle)
		})
	}
	if len(picked) == 0 {
		return nil, fmt.Errorf("github: no release in %s matches %q", repo, needle)
	}
	// The REST API returns releases newest-first across pages, so the first
	// match is the newest one. Sorting would need a timestamp this package
	// deliberately does not model.
	return &picked[0], nil
}

func filterBy(in []Release, keep func(Release) bool) []Release {
	var out []Release
	for _, r := range in {
		if keep(r) {
			out = append(out, r)
		}
	}
	return out
}

func (c *Client) get(path string) (*Release, error) {
	body, err := c.fetchRaw(path)
	if err != nil {
		return nil, err
	}
	var r Release
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("github: decode %s: %w", path, err)
	}
	return &r, nil
}

// fetchRaw performs a GET with retries for transient failures and turns API
// error payloads into Go errors.
func (c *Client) fetchRaw(path string) ([]byte, error) {
	url := c.BaseURL + path
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * time.Second)
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("github: build request %s: %w", url, err)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("github: GET %s: %w", url, err)
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("github: read %s: %w", url, readErr)
			continue
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, rateLimitErr(body)
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("github: GET %s: %s", url, resp.Status)
			continue
		}
		if resp.StatusCode >= 400 {
			return nil, apiError(url, resp.Status, body)
		}
		return body, nil
	}
	return nil, lastErr
}

func rateLimitErr(body []byte) error {
	var r Release
	if json.Unmarshal(body, &r) == nil && strings.Contains(strings.ToLower(r.Message), "rate limit") {
		return ErrRateLimited
	}
	return ErrRateLimited
}

func apiError(url, status string, body []byte) error {
	var r Release
	if json.Unmarshal(body, &r) == nil && r.Message != "" {
		return fmt.Errorf("github: GET %s: %s: %s", url, status, r.Message)
	}
	return fmt.Errorf("github: GET %s: %s", url, status)
}
