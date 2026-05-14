package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"actupdate/internal/actionspec"
)

const DefaultBaseURL = "https://api.github.com"

type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	cache      map[string][]actionspec.StableVersion
	tagTimes   map[string]time.Time
	now        func() time.Time
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

type gitRefResponse struct {
	Object gitObject `json:"object"`
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitTagResponse struct {
	Tagger gitSignature `json:"tagger"`
}

type gitCommitResponse struct {
	Author    gitSignature `json:"author"`
	Committer gitSignature `json:"committer"`
}

type gitSignature struct {
	Date string `json:"date"`
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
		tagTimes:   map[string]time.Time{},
		now:        time.Now,
	}
}

func (c *Client) ResolveLatestMajor(ctx context.Context, repo string, currentMajor int, cooldown time.Duration) (Resolution, error) {
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
		return Resolution{
			HasUpgrade:  false,
			LatestMajor: latestMajor,
			Reason:      "already on latest stable major",
		}, nil
	}

	eligibleTags := tags
	if cooldown > 0 {
		cutoff := c.now().Add(-cooldown)
		eligibleTags = make([]actionspec.StableVersion, 0, len(tags))
		for _, tag := range tags {
			if tag.Major <= currentMajor {
				eligibleTags = append(eligibleTags, tag)
				continue
			}
			publishedAt, err := c.tagPublishedAt(ctx, repo, tag.Original)
			if err != nil {
				return Resolution{}, err
			}
			if !publishedAt.After(cutoff) {
				eligibleTags = append(eligibleTags, tag)
			}
		}
	}

	latestEligibleMajor := currentMajor
	for _, tag := range eligibleTags {
		if tag.Major > latestEligibleMajor {
			latestEligibleMajor = tag.Major
		}
	}
	if latestEligibleMajor <= currentMajor {
		return Resolution{
			HasUpgrade:  false,
			LatestMajor: latestMajor,
			Reason:      "newer major tags are still within cooldown",
		}, nil
	}

	if moving, ok := findMovingMajor(eligibleTags, latestEligibleMajor); ok {
		return Resolution{
			TargetRef:   moving.Original,
			HasUpgrade:  true,
			Reason:      "moving major tag",
			LatestMajor: latestEligibleMajor,
		}, nil
	}

	best, ok := findHighestForMajor(eligibleTags, latestEligibleMajor)
	if !ok {
		return Resolution{}, fmt.Errorf("%s: no stable tag found for latest eligible major v%d", repo, latestEligibleMajor)
	}
	return Resolution{
		TargetRef:   best.Original,
		HasUpgrade:  true,
		Reason:      "exact tag fallback",
		LatestMajor: latestEligibleMajor,
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
		var tags []tagResponse
		if err := c.getJSON(req, repo, "repository not found", &tags); err != nil {
			return nil, err
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

func (c *Client) tagPublishedAt(ctx context.Context, repo, tag string) (time.Time, error) {
	cacheKey := repo + "@" + tag
	if cached, ok := c.tagTimes[cacheKey]; ok {
		return cached, nil
	}

	refEndpoint, err := url.Parse(fmt.Sprintf("%s/repos/%s/git/ref/tags/%s", c.baseURL, repo, tag))
	if err != nil {
		return time.Time{}, err
	}
	refReq, err := http.NewRequestWithContext(ctx, http.MethodGet, refEndpoint.String(), nil)
	if err != nil {
		return time.Time{}, err
	}

	var ref gitRefResponse
	if err := c.getJSON(refReq, repo, fmt.Sprintf("tag ref %s not found", tag), &ref); err != nil {
		return time.Time{}, err
	}

	var publishedAt time.Time
	switch ref.Object.Type {
	case "tag":
		publishedAt, err = c.annotatedTagTime(ctx, repo, ref.Object.SHA)
	case "commit":
		publishedAt, err = c.commitTime(ctx, repo, ref.Object.SHA)
	default:
		err = fmt.Errorf("%s: unsupported git ref object type %q for tag %s", repo, ref.Object.Type, tag)
	}
	if err != nil {
		return time.Time{}, err
	}

	c.tagTimes[cacheKey] = publishedAt
	return publishedAt, nil
}

func (c *Client) annotatedTagTime(ctx context.Context, repo, sha string) (time.Time, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/repos/%s/git/tags/%s", c.baseURL, repo, sha))
	if err != nil {
		return time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return time.Time{}, err
	}

	var tag gitTagResponse
	if err := c.getJSON(req, repo, fmt.Sprintf("annotated tag %s not found", sha), &tag); err != nil {
		return time.Time{}, err
	}
	return parseGitHubTime(repo, tag.Tagger.Date, "tagger")
}

func (c *Client) commitTime(ctx context.Context, repo, sha string) (time.Time, error) {
	endpoint, err := url.Parse(fmt.Sprintf("%s/repos/%s/git/commits/%s", c.baseURL, repo, sha))
	if err != nil {
		return time.Time{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return time.Time{}, err
	}

	var commit gitCommitResponse
	if err := c.getJSON(req, repo, fmt.Sprintf("commit %s not found", sha), &commit); err != nil {
		return time.Time{}, err
	}
	if commit.Committer.Date != "" {
		return parseGitHubTime(repo, commit.Committer.Date, "committer")
	}
	return parseGitHubTime(repo, commit.Author.Date, "author")
}

func (c *Client) getJSON(req *http.Request, repo, notFoundMessage string, out any) error {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "actupdate")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed: %w", repo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %s", repo, notFoundMessage)
	}
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: GitHub API rate limited or forbidden", repo)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: unexpected GitHub API status %d", repo, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: failed to decode GitHub response: %w", repo, err)
	}
	return nil
}

func parseGitHubTime(repo, value, field string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("%s: missing %s timestamp in GitHub response", repo, field)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: invalid %s timestamp %q: %w", repo, field, value, err)
	}
	return parsed, nil
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
