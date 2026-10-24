// Package aur talks to the Arch User Repository: it answers "does this package
// exist?" and manages the per-package git repository on disk.
package aur

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Info is the subset of the RPC v5 /info response auto-aur uses.
type Info struct {
	Name       string   `json:"Name"`
	Version    string   `json:"Version"`
	Maintainer *string  `json:"Maintainer"`
	Depends    []string `json:"Depends"`
	Conflicts  []string `json:"Conflicts"`
	Provides   []string `json:"Provides"`
	OutOfDate  *int64   `json:"OutOfDate"`
}

type infoResponse struct {
	ResultCount int    `json:"resultcount"`
	Results     []Info `json:"results"`
	Type        string `json:"type"`
}

// Client queries the AUR RPC endpoint.
type Client struct {
	BaseURL string // e.g. https://aur.archlinux.org
	HTTP    *http.Client
}

// NewClient builds an RPC client for host.
func NewClient(host string) *Client {
	return &Client{
		BaseURL: "https://" + host,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// Lookup returns package metadata and whether the package exists.
//
// The AUR answers an unknown package with {"resultcount":0,"results":[]} and a
// 200 status, so existence must be judged from the payload rather than from the
// HTTP status. The cgit web frontend used to serve package tarballs is not a
// valid substitute: it answers 200 for names that do not exist.
func (c *Client) Lookup(name string) (Info, bool, error) {
	url := fmt.Sprintf("%s/rpc/v5/info?arg[]=%s", c.BaseURL, name)
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return Info{}, false, fmt.Errorf("aur: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return Info{}, false, fmt.Errorf("aur: read %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return Info{}, false, fmt.Errorf("aur: GET %s: %s", url, resp.Status)
	}
	var out infoResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return Info{}, false, fmt.Errorf("aur: decode %s: %w", url, err)
	}
	if out.ResultCount == 0 || len(out.Results) == 0 {
		return Info{}, false, nil
	}
	// The endpoint is a batch query; match the requested name case-sensitively
	// because RPC echoes every hit back in arbitrary order.
	for _, r := range out.Results {
		if r.Name == name {
			return r, true, nil
		}
	}
	return Info{}, false, nil
}
