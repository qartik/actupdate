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

type Precision int

const (
	PrecisionMovingMajor Precision = iota
	PrecisionMovingMinor
	PrecisionExact
)

type Version struct {
	Original  string
	Major     int
	Minor     int
	Patch     int
	Precision Precision
}

type Eligibility int

const (
	EligibilityUnknown Eligibility = iota
	EligibilityEligible
	EligibilityBlocked
)

type Candidate struct {
	Version     Version
	Eligibility Eligibility
}

type majorCandidates struct {
	Major       int
	MovingMajor *Candidate
	MovingMinor *Candidate
	Exact       *Candidate
}

type Decision struct {
	Target         Version
	HasUpgrade     bool
	NoUpdateReason NoUpdateReason
}

type NoUpdateReason string

const (
	reasonAlreadyLatestMajor    NoUpdateReason = "already on latest stable major"
	reasonAlreadyLatestStable   NoUpdateReason = "already on latest stable version"
	reasonNewerMajorCooldown    NoUpdateReason = "newer major tags are still within cooldown"
	reasonCurrentMovingCooldown NoUpdateReason = "newer moving tags in current major are still within cooldown"
	reasonCurrentStableCooldown NoUpdateReason = "newer stable tags in current major are still within cooldown"
	reasonMovingMajorTag        NoUpdateReason = "moving major tag"
	reasonMovingMinorTag        NoUpdateReason = "moving minor tag"
	reasonExactFallback         NoUpdateReason = "exact tag fallback"
	reasonCurrentMajorMoving    NoUpdateReason = "newer moving version in current major"
	reasonCurrentMajorStable    NoUpdateReason = "newer stable version in current major"
)

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

	current := Version{Major: currentMajor, Precision: PrecisionMovingMajor}
	return c.resolve(ctx, repo, current, cooldown, true, tags)
}

func (c *Client) ResolveLatestStable(ctx context.Context, repo string, current actionspec.StableVersion, cooldown time.Duration) (Resolution, error) {
	tags, err := c.stableTags(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	if len(tags) == 0 {
		return Resolution{}, fmt.Errorf("%s: no stable semver tags found", repo)
	}

	return c.resolve(ctx, repo, versionFromStable(current), cooldown, false, tags)
}

func (c *Client) resolve(ctx context.Context, repo string, current Version, cooldown time.Duration, majorOnly bool, tags []actionspec.StableVersion) (Resolution, error) {
	candidates := collectMajorCandidates(tags)
	cutoff := time.Time{}
	if cooldown > 0 {
		cutoff = c.now().Add(-cooldown)
	}
	eligible := func(candidate *Candidate) (bool, error) {
		return c.ensureEligible(ctx, repo, candidate, cutoff)
	}
	decision, err := resolvePolicy(current, candidates, majorOnly, eligible)
	if err != nil {
		return Resolution{}, err
	}
	return resolutionFromDecision(decision, latestPublishedMajorFromCandidates(candidates)), nil
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

	slices.SortFunc(versions, compareStableVersionDesc)
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

func (c *Client) ensureEligible(ctx context.Context, repo string, candidate *Candidate, cutoff time.Time) (bool, error) {
	if candidate == nil {
		return false, nil
	}
	if candidate.Eligibility != EligibilityUnknown {
		return candidate.Eligibility == EligibilityEligible, nil
	}
	if cutoff.IsZero() {
		candidate.Eligibility = EligibilityEligible
		return true, nil
	}
	publishedAt, err := c.tagPublishedAt(ctx, repo, candidate.Version.Original)
	if err != nil {
		return false, err
	}
	if publishedAt.After(cutoff) {
		candidate.Eligibility = EligibilityBlocked
		return false, nil
	}
	candidate.Eligibility = EligibilityEligible
	return true, nil
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

func resolvePolicy(current Version, candidates []majorCandidates, majorOnly bool, eligible func(*Candidate) (bool, error)) (Decision, error) {
	if latestPublishedMajorFromCandidates(candidates) <= current.Major {
		if majorOnly {
			return noUpdate(reasonAlreadyLatestMajor), nil
		}
		return resolveCurrentMajor(current, candidates, eligible)
	}

	for i := range candidates {
		candidate := &candidates[i]
		if candidate.Major <= current.Major {
			continue
		}
		if decision, decided, err := firstEligibleUpgrade([]*Candidate{candidate.MovingMajor, candidate.MovingMinor, candidate.Exact}, eligible); err != nil || decided {
			return decision, err
		}
	}

	if majorOnly {
		return noUpdate(reasonNewerMajorCooldown), nil
	}
	currentDecision, err := resolveCurrentMajor(current, candidates, eligible)
	if err != nil {
		return Decision{}, err
	}
	if currentDecision.HasUpgrade {
		return currentDecision, nil
	}
	return noUpdate(reasonNewerMajorCooldown), nil
}

func resolveCurrentMajor(current Version, candidates []majorCandidates, eligible func(*Candidate) (bool, error)) (Decision, error) {
	candidate, ok := findMajorCandidates(candidates, current.Major)
	if !ok {
		return noUpdate(reasonAlreadyLatestStable), nil
	}

	switch current.Precision {
	case PrecisionMovingMajor:
		if isSameMajorMovingUpgrade(current, candidate.MovingMajor) {
			return decisionForCandidate(candidate.MovingMajor, reasonCurrentMajorMoving, reasonCurrentMovingCooldown, eligible)
		}
	case PrecisionMovingMinor:
		if isSameMajorMovingUpgrade(current, candidate.MovingMinor) {
			return decisionForCandidate(candidate.MovingMinor, reasonCurrentMajorMoving, reasonCurrentMovingCooldown, eligible)
		}
	case PrecisionExact:
		return firstEligibleCurrentMajorExactUpgrade(current, candidate, eligible)
	}
	return noUpdate(reasonAlreadyLatestStable), nil
}

func firstEligibleCurrentMajorExactUpgrade(current Version, candidate majorCandidates, eligible func(*Candidate) (bool, error)) (Decision, error) {
	foundBlocked := false
	for _, target := range []*Candidate{candidate.MovingMajor, candidate.MovingMinor, candidate.Exact} {
		if !isSameMajorExactCurrentUpgrade(current, target) {
			continue
		}
		isEligible, err := eligible(target)
		if err != nil {
			return Decision{}, err
		}
		if isEligible {
			if target.Version.Precision == PrecisionExact {
				return upgrade(target.Version, reasonCurrentMajorStable), nil
			}
			return upgrade(target.Version, reasonCurrentMajorMoving), nil
		}
		foundBlocked = true
	}
	if foundBlocked {
		return noUpdate(reasonCurrentStableCooldown), nil
	}
	return noUpdate(reasonAlreadyLatestStable), nil
}

func firstEligibleUpgrade(ordered []*Candidate, eligible func(*Candidate) (bool, error)) (Decision, bool, error) {
	for _, candidate := range ordered {
		if candidate == nil {
			continue
		}
		isEligible, err := eligible(candidate)
		if err != nil {
			return Decision{}, false, err
		}
		if isEligible {
			return upgrade(candidate.Version, upgradeReason(candidate.Version.Precision)), true, nil
		}
	}
	return Decision{}, false, nil
}

func decisionForCandidate(candidate *Candidate, upgradeReason, blockedReason NoUpdateReason, eligible func(*Candidate) (bool, error)) (Decision, error) {
	if candidate == nil {
		return noUpdate(reasonAlreadyLatestStable), nil
	}
	isEligible, err := eligible(candidate)
	if err != nil {
		return Decision{}, err
	}
	if isEligible {
		return upgrade(candidate.Version, upgradeReason), nil
	}
	return noUpdate(blockedReason), nil
}

func resolutionFromDecision(decision Decision, latestMajor int) Resolution {
	resolution := Resolution{
		HasUpgrade:  decision.HasUpgrade,
		LatestMajor: latestMajor,
		Reason:      string(decision.NoUpdateReason),
	}
	if decision.HasUpgrade {
		resolution.TargetRef = decision.Target.Original
	}
	return resolution
}

func noUpdate(reason NoUpdateReason) Decision {
	return Decision{NoUpdateReason: reason}
}

func upgrade(target Version, reason NoUpdateReason) Decision {
	return Decision{Target: target, HasUpgrade: true, NoUpdateReason: reason}
}

func upgradeReason(precision Precision) NoUpdateReason {
	switch precision {
	case PrecisionMovingMajor:
		return reasonMovingMajorTag
	case PrecisionMovingMinor:
		return reasonMovingMinorTag
	default:
		return reasonExactFallback
	}
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

func collectMajorCandidates(tags []actionspec.StableVersion) []majorCandidates {
	byMajor := make([]majorCandidates, 0)
	indexByMajor := map[int]int{}
	for _, tag := range tags {
		version := versionFromStable(tag)
		index, ok := indexByMajor[version.Major]
		if !ok {
			index = len(byMajor)
			indexByMajor[version.Major] = index
			byMajor = append(byMajor, majorCandidates{Major: version.Major})
		}

		slot := &byMajor[index]
		candidate := &Candidate{Version: version}
		switch version.Precision {
		case PrecisionMovingMajor:
			if slot.MovingMajor == nil {
				slot.MovingMajor = candidate
			}
		case PrecisionMovingMinor:
			if slot.MovingMinor == nil {
				slot.MovingMinor = candidate
			}
		case PrecisionExact:
			if slot.Exact == nil {
				slot.Exact = candidate
			}
		}
	}
	return byMajor
}

func findMajorCandidates(candidates []majorCandidates, major int) (majorCandidates, bool) {
	for _, candidate := range candidates {
		if candidate.Major == major {
			return candidate, true
		}
	}
	return majorCandidates{}, false
}

func versionFromStable(version actionspec.StableVersion) Version {
	return Version{
		Original:  version.Original,
		Major:     version.Major,
		Minor:     version.Minor,
		Patch:     version.Patch,
		Precision: precisionFromStable(version),
	}
}

func precisionFromStable(version actionspec.StableVersion) Precision {
	if !version.HasMinor {
		return PrecisionMovingMajor
	}
	if !version.HasPatch {
		return PrecisionMovingMinor
	}
	return PrecisionExact
}

func isSameMajorExactCurrentUpgrade(current Version, candidate *Candidate) bool {
	if candidate == nil || current.Major != candidate.Version.Major {
		return false
	}
	if current.Precision != PrecisionExact {
		return false
	}
	return compareNumericVersion(current, candidate.Version) < 0
}

func isSameMajorMovingUpgrade(current Version, candidate *Candidate) bool {
	if candidate == nil || current.Major != candidate.Version.Major {
		return false
	}
	if current.Precision != candidate.Version.Precision {
		return false
	}
	return compareNumericVersion(current, candidate.Version) < 0
}

func compareStableVersionDesc(a, b actionspec.StableVersion) int {
	return compareVersionDesc(versionFromStable(a), versionFromStable(b))
}

func compareVersionDesc(a, b Version) int {
	if a.Major != b.Major {
		return b.Major - a.Major
	}
	if a.Minor != b.Minor {
		return b.Minor - a.Minor
	}
	if a.Patch != b.Patch {
		return b.Patch - a.Patch
	}
	if a.Precision != b.Precision {
		return int(b.Precision) - int(a.Precision)
	}
	if strings.HasPrefix(a.Original, "v") && !strings.HasPrefix(b.Original, "v") {
		return -1
	}
	if !strings.HasPrefix(a.Original, "v") && strings.HasPrefix(b.Original, "v") {
		return 1
	}
	return strings.Compare(a.Original, b.Original)
}

func compareNumericVersion(a, b Version) int {
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

func latestPublishedMajorFromCandidates(candidates []majorCandidates) int {
	if len(candidates) == 0 {
		return 0
	}
	return candidates[0].Major
}
