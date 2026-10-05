package github

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qartik/actupdate/internal/actionspec"
)

// ResolvePinned selects an eligible exact release without downgrading current.
// Existing exact pins only advance to newer versions, never repointed tags.
func (c *Client) ResolvePinned(ctx context.Context, repo string, current actionspec.StableVersion, alreadyPinned bool, cooldown time.Duration) (Resolution, error) {
	tags, err := c.stableTags(ctx, repo)
	if err != nil {
		return Resolution{}, err
	}
	cutoff := time.Time{}
	if cooldown > 0 {
		cutoff = c.now().Add(-cooldown)
	}
	hasExact, blocked := false, false
	for _, tag := range tags {
		if !tag.HasPatch {
			continue
		}
		hasExact = true
		comparison := compareNumericVersion(versionFromStable(tag), versionFromStable(current))
		if comparison < 0 || (comparison == 0 && alreadyPinned && current.HasPatch) {
			continue
		}
		eligible, err := c.tagEligible(ctx, repo, tag.Original, cutoff)
		if err != nil {
			return Resolution{}, err
		}
		if !eligible {
			blocked = true
			continue
		}
		sha, err := c.tagCommit(ctx, repo, tag.Original)
		if err != nil {
			return Resolution{}, err
		}
		return Resolution{TargetRef: sha, TargetTag: tag.Original, HasUpgrade: true, Reason: "latest eligible exact release"}, nil
	}
	if !hasExact {
		return Resolution{Skipped: true, Reason: "no stable exact semver tags found"}, nil
	}
	if blocked {
		return Resolution{Reason: "candidate exact releases are still within cooldown"}, nil
	}
	return Resolution{Reason: "already on latest eligible exact release"}, nil
}

func (c *Client) tagRef(ctx context.Context, repo, tag string) (gitObject, error) {
	key := repo + "@" + tag
	if object, ok := c.refs[key]; ok {
		return object, nil
	}
	var ref gitRefResponse
	if err := c.getRepositoryJSON(ctx, repo, fmt.Sprintf("tag ref %s not found", tag), &ref, "git", "ref", "tags", tag); err != nil {
		return gitObject{}, err
	}
	c.refs[key] = ref.Object
	return ref.Object, nil
}

func (c *Client) annotatedTag(ctx context.Context, repo, sha string) (gitTagResponse, error) {
	key := repo + "@" + sha
	if tag, ok := c.tagObjects[key]; ok {
		return tag, nil
	}
	var tag gitTagResponse
	if err := c.getRepositoryJSON(ctx, repo, fmt.Sprintf("annotated tag %s not found", sha), &tag, "git", "tags", sha); err != nil {
		return gitTagResponse{}, err
	}
	c.tagObjects[key] = tag
	return tag, nil
}

func (c *Client) tagCommit(ctx context.Context, repo, tag string) (string, error) {
	object, err := c.tagRef(ctx, repo, tag)
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for depth := 0; depth < 100; depth++ {
		if !actionspec.IsCommitSHA(object.SHA) {
			return "", fmt.Errorf("%s: invalid full SHA for tag %s", repo, tag)
		}
		sha := strings.ToLower(object.SHA)
		if seen[sha] {
			return "", fmt.Errorf("%s: cyclic annotated tag chain for %s", repo, tag)
		}
		seen[sha] = true
		switch object.Type {
		case "commit":
			return sha, nil
		case "tag":
			annotated, err := c.annotatedTag(ctx, repo, object.SHA)
			if err != nil {
				return "", err
			}
			object = annotated.Object
		default:
			return "", fmt.Errorf("%s: unsupported git object type %q for tag %s", repo, object.Type, tag)
		}
	}
	return "", fmt.Errorf("%s: annotated tag chain too deep for %s", repo, tag)
}
