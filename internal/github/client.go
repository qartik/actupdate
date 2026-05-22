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

type majorCandidates struct {
	Major               int
	MovingMajor         actionspec.StableVersion
	HasMovingMajor      bool
	MovingMajorEligible bool
	MovingMinor         actionspec.StableVersion
	HasMovingMinor      bool
	MovingMinorEligible bool
	Exact               actionspec.StableVersion
	HasExact            bool
	ExactEligible       bool
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

	cutoff := time.Time{}
	if cooldown > 0 {
		cutoff = c.now().Add(-cooldown)
	}
	candidates := collectMajorCandidates(tags)
	if err := c.populateEligibilityForNewerMajors(ctx, repo, candidates, currentMajor, cutoff); err != nil {
		return Resolution{}, err
	}
	return resolveLatestMajorPolicy(currentMajor, candidates), nil
}

func (c *Client) ResolveLatestStable(ctx context.Context, repo string, current actionspec.StableVersion, cooldown time.Duration) (Resolution, error) {
	tags, err := c.stableTags(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	if len(tags) == 0 {
		return Resolution{}, fmt.Errorf("%s: no stable semver tags found", repo)
	}

	cutoff := time.Time{}
	if cooldown > 0 {
		cutoff = c.now().Add(-cooldown)
	}
	candidates := collectMajorCandidates(tags)
	if err := c.populateEligibilityForNewerMajors(ctx, repo, candidates, current.Major, cutoff); err != nil {
		return Resolution{}, err
	}
	if err := c.populateEligibilityForCurrentMajorUpgrades(ctx, repo, candidates, current, cutoff); err != nil {
		return Resolution{}, err
	}
	return resolveLatestStablePolicy(current, candidates), nil
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

	refEndpoint, err := c.endpointURL(repo, "git", "ref", "tags", tag)
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
	endpoint, err := c.endpointURL(repo, "git", "tags", sha)
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
	endpoint, err := c.endpointURL(repo, "git", "commits", sha)
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

func (c *Client) tagEligible(ctx context.Context, repo, tag string, cutoff time.Time) (bool, error) {
	if cutoff.IsZero() {
		return true, nil
	}
	publishedAt, err := c.tagPublishedAt(ctx, repo, tag)
	if err != nil {
		return false, err
	}
	return !publishedAt.After(cutoff), nil
}

func (c *Client) endpointURL(repo string, segments ...string) (*url.URL, error) {
	base := strings.TrimRight(c.baseURL, "/")
	path := strings.Builder{}
	path.WriteString(base)
	path.WriteString("/repos/")

	repoParts := strings.Split(repo, "/")
	for i, part := range repoParts {
		if i > 0 {
			path.WriteByte('/')
		}
		path.WriteString(url.PathEscape(part))
	}
	for _, segment := range segments {
		path.WriteByte('/')
		path.WriteString(url.PathEscape(segment))
	}
	return url.Parse(path.String())
}

func newerMajors(tags []actionspec.StableVersion, currentMajor int) []int {
	seen := map[int]struct{}{}
	majors := make([]int, 0)
	for _, tag := range tags {
		if tag.Major <= currentMajor {
			continue
		}
		if _, ok := seen[tag.Major]; ok {
			continue
		}
		seen[tag.Major] = struct{}{}
		majors = append(majors, tag.Major)
	}
	return majors
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

func (c *Client) populateEligibilityForNewerMajors(ctx context.Context, repo string, candidates []majorCandidates, currentMajor int, cutoff time.Time) error {
	for i := range candidates {
		if candidates[i].Major <= currentMajor {
			continue
		}
		if candidates[i].HasMovingMajor {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].MovingMajor.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].MovingMajorEligible = eligible
		}
		if candidates[i].HasMovingMinor {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].MovingMinor.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].MovingMinorEligible = eligible
		}
		if candidates[i].HasExact {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].Exact.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].ExactEligible = eligible
		}
	}
	return nil
}

func (c *Client) populateEligibilityForCurrentMajorUpgrades(ctx context.Context, repo string, candidates []majorCandidates, current actionspec.StableVersion, cutoff time.Time) error {
	for i := range candidates {
		if candidates[i].Major != current.Major {
			continue
		}
		if isMajorMovingRef(current) && candidates[i].HasMovingMajor && isSameMajorMovingUpgrade(current, candidates[i].MovingMajor) {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].MovingMajor.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].MovingMajorEligible = eligible
		}
		if isMinorMovingRef(current) && candidates[i].HasMovingMinor && isSameMajorMovingUpgrade(current, candidates[i].MovingMinor) {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].MovingMinor.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].MovingMinorEligible = eligible
		}
		if candidates[i].HasExact && isSameMajorExactUpgrade(current, candidates[i].Exact) {
			eligible, err := c.tagEligible(ctx, repo, candidates[i].Exact.Original, cutoff)
			if err != nil {
				return err
			}
			candidates[i].ExactEligible = eligible
		}
		return nil
	}
	return nil
}

func resolveLatestMajorPolicy(currentMajor int, candidates []majorCandidates) Resolution {
	latestMajor := latestPublishedMajorFromCandidates(candidates)
	if latestMajor <= currentMajor {
		return Resolution{
			HasUpgrade:  false,
			LatestMajor: latestMajor,
			Reason:      "already on latest stable major",
		}
	}

	for _, candidate := range candidates {
		if candidate.Major <= currentMajor {
			continue
		}
		if movingRef, eligible, ok := preferredMovingUpgrade(candidate); ok {
			if eligible {
				return Resolution{
					TargetRef:   movingRef.Original,
					HasUpgrade:  true,
					Reason:      "moving major tag",
					LatestMajor: latestMajor,
				}
			}
		}
		if candidate.HasExact {
			if candidate.ExactEligible {
				return Resolution{
					TargetRef:   candidate.Exact.Original,
					HasUpgrade:  true,
					Reason:      "exact tag fallback",
					LatestMajor: latestMajor,
				}
			}
		}
	}

	return Resolution{
		HasUpgrade:  false,
		LatestMajor: latestMajor,
		Reason:      "newer major tags are still within cooldown",
	}
}

func resolveLatestStablePolicy(current actionspec.StableVersion, candidates []majorCandidates) Resolution {
	latestMajor := latestPublishedMajorFromCandidates(candidates)
	foundBlockedNewerMajor := false
	var currentMajor majorCandidates
	hasCurrentMajor := false

	for _, candidate := range candidates {
		if candidate.Major == current.Major {
			currentMajor = candidate
			hasCurrentMajor = true
		}
		if candidate.Major <= current.Major {
			continue
		}
		if movingRef, eligible, ok := preferredMovingUpgrade(candidate); ok {
			if eligible {
				return Resolution{
					TargetRef:   movingRef.Original,
					HasUpgrade:  true,
					Reason:      "moving major tag",
					LatestMajor: latestMajor,
				}
			}
			foundBlockedNewerMajor = true
		}
		if candidate.HasExact {
			if candidate.ExactEligible {
				return Resolution{
					TargetRef:   candidate.Exact.Original,
					HasUpgrade:  true,
					Reason:      "exact tag fallback",
					LatestMajor: latestMajor,
				}
			}
			foundBlockedNewerMajor = true
		}
	}

	if hasCurrentMajor {
		if movingTarget, eligible, ok := samePrecisionMovingCandidate(current, currentMajor); ok && isSameMajorMovingUpgrade(current, movingTarget) {
			if eligible {
				return Resolution{
					TargetRef:   movingTarget.Original,
					HasUpgrade:  true,
					Reason:      "newer moving version in current major",
					LatestMajor: latestMajor,
				}
			}
			return Resolution{
				HasUpgrade:  false,
				LatestMajor: latestMajor,
				Reason:      "newer moving tags in current major are still within cooldown",
			}
		}
	}
	if hasCurrentMajor && currentMajor.HasExact && isSameMajorExactUpgrade(current, currentMajor.Exact) {
		if currentMajor.ExactEligible {
			return Resolution{
				TargetRef:   currentMajor.Exact.Original,
				HasUpgrade:  true,
				Reason:      "newer stable version in current major",
				LatestMajor: latestMajor,
			}
		}
		return Resolution{
			HasUpgrade:  false,
			LatestMajor: latestMajor,
			Reason:      "newer stable tags in current major are still within cooldown",
		}
	}
	if foundBlockedNewerMajor {
		return Resolution{
			HasUpgrade:  false,
			LatestMajor: latestMajor,
			Reason:      "newer major tags are still within cooldown",
		}
	}

	return Resolution{
		HasUpgrade:  false,
		LatestMajor: latestMajor,
		Reason:      "already on latest stable version",
	}
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
	if a.HasMinor != b.HasMinor {
		if a.HasMinor {
			return -1
		}
		return 1
	}
	if a.HasPatch != b.HasPatch {
		if a.HasPatch {
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
		if tag.Major == major && isMajorMovingRef(tag) {
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

func isMovingRef(tag actionspec.StableVersion) bool {
	return !tag.HasPatch
}

func isMajorMovingRef(tag actionspec.StableVersion) bool {
	return !tag.HasMinor && !tag.HasPatch
}

func isMinorMovingRef(tag actionspec.StableVersion) bool {
	return tag.HasMinor && !tag.HasPatch
}

func isSameMajorExactUpgrade(current, candidate actionspec.StableVersion) bool {
	if current.Major != candidate.Major {
		return false
	}
	if isMovingRef(current) || isMovingRef(candidate) {
		return false
	}
	return compareNumericVersion(current, candidate) < 0
}

func isSameMajorMovingUpgrade(current, candidate actionspec.StableVersion) bool {
	if current.Major != candidate.Major {
		return false
	}
	if !isMovingRef(current) || !isMovingRef(candidate) {
		return false
	}
	return compareVersionDesc(current, candidate) > 0
}

func compareNumericVersion(a, b actionspec.StableVersion) int {
	if a.Major != b.Major {
		if a.Major < b.Major {
			return -1
		}
		return 1
	}
	if a.Minor != b.Minor {
		if a.Minor < b.Minor {
			return -1
		}
		return 1
	}
	if a.Patch != b.Patch {
		if a.Patch < b.Patch {
			return -1
		}
		return 1
	}
	return 0
}

func collectMajorCandidates(tags []actionspec.StableVersion) []majorCandidates {
	byMajor := make([]majorCandidates, 0)
	indexByMajor := map[int]int{}
	for _, tag := range tags {
		index, ok := indexByMajor[tag.Major]
		if !ok {
			index = len(byMajor)
			indexByMajor[tag.Major] = index
			byMajor = append(byMajor, majorCandidates{Major: tag.Major})
		}
		candidate := &byMajor[index]
		if isMajorMovingRef(tag) {
			if !candidate.HasMovingMajor {
				candidate.MovingMajor = tag
				candidate.HasMovingMajor = true
			}
			continue
		}
		if isMinorMovingRef(tag) {
			if !candidate.HasMovingMinor {
				candidate.MovingMinor = tag
				candidate.HasMovingMinor = true
			}
			continue
		}
		if !candidate.HasExact {
			candidate.Exact = tag
			candidate.HasExact = true
		}
	}
	return byMajor
}

func preferredMovingUpgrade(candidate majorCandidates) (actionspec.StableVersion, bool, bool) {
	if candidate.HasMovingMajor {
		return candidate.MovingMajor, candidate.MovingMajorEligible, true
	}
	if candidate.HasMovingMinor {
		return candidate.MovingMinor, candidate.MovingMinorEligible, true
	}
	return actionspec.StableVersion{}, false, false
}

func samePrecisionMovingCandidate(current actionspec.StableVersion, candidate majorCandidates) (actionspec.StableVersion, bool, bool) {
	if isMajorMovingRef(current) {
		if candidate.HasMovingMajor {
			return candidate.MovingMajor, candidate.MovingMajorEligible, true
		}
		return actionspec.StableVersion{}, false, false
	}
	if isMinorMovingRef(current) {
		if candidate.HasMovingMinor {
			return candidate.MovingMinor, candidate.MovingMinorEligible, true
		}
		return actionspec.StableVersion{}, false, false
	}
	return actionspec.StableVersion{}, false, false
}

func latestPublishedMajorFromCandidates(candidates []majorCandidates) int {
	if len(candidates) == 0 {
		return 0
	}
	return candidates[0].Major
}
