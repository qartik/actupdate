package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"actupdate/internal/actionspec"
)

const DefaultBaseURL = "https://api.github.com"

type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	cache      map[string][]actionspec.StableVersion
}

type Resolution struct {
	TargetRef   string
	HasUpgrade  bool
	Reason      string
	LatestMajor int
}

type tagResponse struct {
	Name string `json:"name"`
}

func NewClient(httpClient *http.Client, baseURL, token string) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		cache:      map[string][]actionspec.StableVersion{},
	}
}

func (c *Client) ResolveLatestMajor(ctx context.Context, repo string, currentMajor int) (Resolution, error) {
	tags, err := c.stableTags(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	if len(tags) == 0 {
		return Resolution{}, fmt.Errorf("%s: no stable semver tags found", repo)
	}

	latestMajor := currentMajor
	for _, tag := range tags {
		if tag.Major > latestMajor {
			latestMajor = tag.Major
		}
	}
	if latestMajor <= currentMajor {
		return Resolution{HasUpgrade: false, LatestMajor: latestMajor}, nil
	}

	if moving, ok := findMovingMajor(tags, latestMajor); ok {
		return Resolution{
			TargetRef:   moving.Original,
			HasUpgrade:  true,
			Reason:      "moving major tag",
			LatestMajor: latestMajor,
		}, nil
	}

	best, ok := findHighestForMajor(tags, latestMajor)
	if !ok {
		return Resolution{}, fmt.Errorf("%s: no stable tag found for latest major v%d", repo, latestMajor)
	}
	return Resolution{
		TargetRef:   best.Original,
		HasUpgrade:  true,
		Reason:      "exact tag fallback",
		LatestMajor: latestMajor,
	}, nil
}

func (c *Client) stableTags(ctx context.Context, repo string) ([]actionspec.StableVersion, error) {
	if cached, ok := c.cache[repo]; ok {
		return cached, nil
	}

	var versions []actionspec.StableVersion
	for page := 1; ; page++ {
		endpoint, err := url.Parse(fmt.Sprintf("%s/repos/%s/tags", c.baseURL, repo))
		if err != nil {
			return nil, err
		}
		query := endpoint.Query()
		query.Set("per_page", "100")
		query.Set("page", fmt.Sprintf("%d", page))
		endpoint.RawQuery = query.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "actupdate")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s: request failed: %w", repo, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%s: repository not found", repo)
		}
		if resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%s: GitHub API rate limited or forbidden", repo)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: unexpected GitHub API status %d", repo, resp.StatusCode)
		}

		var tags []tagResponse
		if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
			return nil, fmt.Errorf("%s: failed to decode tags: %w", repo, err)
		}

		for _, tag := range tags {
			version, err := actionspec.ParseStableVersion(tag.Name)
			if err == nil {
				versions = append(versions, version)
			}
		}

		if len(tags) < 100 {
			break
		}
	}

	slices.SortFunc(versions, compareVersionDesc)
	c.cache[repo] = versions
	return versions, nil
}

func compareVersionDesc(a, b actionspec.StableVersion) int {
	if a.Major != b.Major {
		return b.Major - a.Major
	}
	if a.Minor != b.Minor {
		return b.Minor - a.Minor
	}
	if a.Patch != b.Patch {
		return b.Patch - a.Patch
	}
	if isMovingMajor(a) != isMovingMajor(b) {
		if isMovingMajor(a) {
			return -1
		}
		return 1
	}
	if strings.HasPrefix(a.Original, "v") && !strings.HasPrefix(b.Original, "v") {
		return -1
	}
	if !strings.HasPrefix(a.Original, "v") && strings.HasPrefix(b.Original, "v") {
		return 1
	}
	return strings.Compare(a.Original, b.Original)
}

func findMovingMajor(tags []actionspec.StableVersion, major int) (actionspec.StableVersion, bool) {
	for _, tag := range tags {
		if tag.Major == major && isMovingMajor(tag) {
			return tag, true
		}
	}
	return actionspec.StableVersion{}, false
}

func findHighestForMajor(tags []actionspec.StableVersion, major int) (actionspec.StableVersion, bool) {
	for _, tag := range tags {
		if tag.Major == major {
			return tag, true
		}
	}
	return actionspec.StableVersion{}, false
}

func isMovingMajor(tag actionspec.StableVersion) bool {
	return !tag.HasMinor && !tag.HasPatch
}
