package github

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const releaseList = `[
  {"tag_name":"v6.4.0-beta02","name":"6.4.0-beta02","assets":[{"name":"ani-linux-x86_64.appimage","browser_download_url":"https://x/appimage"}]},
  {"tag_name":"v6.4.0","name":"6.4.0","assets":[]},
  {"tag_name":"v6.3.0-alpha01","name":"6.3.0-alpha01","assets":[]}
]`

func server(t *testing.T, body string, status int, seen *http.Header) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			*seen = r.Header.Clone()
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), Pages: 3}
}

func TestFetchSearchPrefersName(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r", Search: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v6.3.0-alpha01" {
		t.Fatalf("got %q", rel.TagName)
	}
}

// Upstream release names omit the channel ("6.4.0-beta02"), so tag matching has
// to work as a fallback, otherwise beta releases are never found.
func TestFetchSearchFallsBackToTag(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r", Search: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v6.4.0-beta02" {
		t.Fatalf("got %q", rel.TagName)
	}
}

func TestFetchLatest(t *testing.T) {
	c := server(t, `{"tag_name":"v9.9.9","name":"9.9.9","assets":[]}`, 200, nil)
	rel, err := c.Fetch(Wanted{Repo: "o/r"})
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v9.9.9" {
		t.Fatalf("got %q", rel.TagName)
	}
}

func TestFetchByTag(t *testing.T) {
	c := server(t, `{"tag_name":"v1.2.3","assets":[]}`, 200, nil)
	if _, err := c.Fetch(Wanted{Repo: "o/r", Tag: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
}

func TestTokenIsSent(t *testing.T) {
	var hdr http.Header
	c := server(t, `{"tag_name":"v1","assets":[]}`, 200, &hdr)
	c.Token = "secret-token"
	if _, err := c.Fetch(Wanted{Repo: "o/r"}); err != nil {
		t.Fatal(err)
	}
	if got := hdr.Get("Authorization"); got != "Bearer secret-token" {
		t.Fatalf("authorization = %q", got)
	}
}

func TestRateLimitIsDetected(t *testing.T) {
	c := server(t, `{"message":"API rate limit exceeded for 1.2.3.4."}`, 403, nil)
	_, err := c.Fetch(Wanted{Repo: "o/r"})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("want ErrRateLimited, got %v", err)
	}
}

func TestAPIErrorSurfacesMessage(t *testing.T) {
	c := server(t, `{"message":"Not Found"}`, 404, nil)
	_, err := c.Fetch(Wanted{Repo: "o/r"})
	if err == nil || !strings.Contains(err.Error(), "Not Found") {
		t.Fatalf("want message in error, got %v", err)
	}
}

func TestNoMatchIsAnError(t *testing.T) {
	c := server(t, releaseList, 200, nil)
	if _, err := c.Fetch(Wanted{Repo: "o/r", Search: "gamma"}); err == nil {
		t.Fatal("expected an error when nothing matches")
	}
}

func TestReleaseVersion(t *testing.T) {
	cases := []struct {
		name    string
		rel     Release
		want    string
		wantErr bool
	}{
		{
			name: "rolling tag with version in name",
			rel:  Release{TagName: "canary", Name: "canary v0.10.0"},
			want: "0.10.0",
		},
		{
			name: "version only in body",
			rel:  Release{TagName: "canary", Body: "自动构建预发布。\n\n版本：0.10.0\n提交：abc"},
			want: "0.10.0",
		},
		{
			name: "versioned tag",
			rel:  Release{TagName: "v1.2.3"},
			want: "1.2.3",
		},
		{
			name: "name wins over body",
			rel:  Release{TagName: "canary", Name: "canary v0.10.0", Body: "版本：0.9.9"},
			want: "0.10.0",
		},
		{
			name:    "rolling tag with no version anywhere",
			rel:     Release{TagName: "canary", Name: "canary"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.rel.Version()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("Version()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestAssetSHA256(t *testing.T) {
	withDigest := Asset{Digest: "sha256:20fe986930b25fa290fa2e59cec678be170d1eb0a4829567967648fea884d1d1"}
	if got := withDigest.SHA256(); got != "20fe986930b25fa290fa2e59cec678be170d1eb0a4829567967648fea884d1d1" {
		t.Fatalf("SHA256()=%q", got)
	}
	for _, d := range []string{"", "sha256:tooshort", "md5:abcd", "sha512:" + strings.Repeat("a", 128)} {
		if got := (Asset{Digest: d}).SHA256(); got != "" {
			t.Errorf("digest %q should not yield sha256 %q", d, got)
		}
	}
}

func TestFileVersion(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte(`{"name":"x","version":"0.10.0"}`))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/contents/package.json") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"content":"` + payload + `","encoding":"base64"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	got, err := c.FileVersion("o/r", "main", "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if got != "0.10.0" {
		t.Fatalf("version=%q", got)
	}
}
