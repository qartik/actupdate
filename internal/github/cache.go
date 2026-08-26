package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"actupdate/internal/actionspec"
)

const (
	cacheDirName = "actupdate"
	cacheFileExt = ".json"
	cacheTTL     = 5 * time.Minute
)

type cacheEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Tags      []actionspec.StableVersion `json:"tags"`
}

func cacheDir() (string, error) {
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheBase, cacheDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func cacheFilePath(dir, repo string) string {
	return filepath.Join(dir, repoToFilename(repo)+cacheFileExt)
}

func repoToFilename(repo string) string {
	out := make([]byte, 0, len(repo))
	for _, r := range repo {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '/' {
			out = append(out, byte(r))
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}

func (c *Client) loadCachedTags(repo string) ([]actionspec.StableVersion, bool) {
	dir := c.cacheDir
	if dir == "" {
		var err error
		dir, err = cacheDir()
		if err != nil {
			return nil, false
		}
	}
	path := cacheFilePath(dir, repo)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}
	if time.Since(entry.Timestamp) > cacheTTL {
		_ = os.Remove(path)
		return nil, false
	}
	return entry.Tags, true
}

func (c *Client) storeCachedTags(repo string, tags []actionspec.StableVersion) {
	dir := c.cacheDir
	if dir == "" {
		var err error
		dir, err = cacheDir()
		if err != nil {
			return
		}
	}
	path := cacheFilePath(dir, repo)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	entry := cacheEntry{
		Timestamp: time.Now(),
		Tags:      tags,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return
	}
}
