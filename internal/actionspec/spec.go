package actionspec

import (
	"fmt"
	"regexp"
	"strings"
)

type Kind int

const (
	KindRemote Kind = iota
	KindLocal
	KindDocker
	KindUnsupported
)

type Spec struct {
	Original string
	Repo     string
	Ref      string
	Kind     Kind
}

var shaPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
var versionPattern = regexp.MustCompile(`^v?([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:\+[^-]+)?$`)

func Parse(value string) (Spec, error) {
	value = strings.TrimSpace(value)
	spec := Spec{Original: value}

	switch {
	case value == "":
		return Spec{}, fmt.Errorf("empty action reference")
	case strings.HasPrefix(value, "./"):
		spec.Kind = KindLocal
		return spec, nil
	case strings.HasPrefix(value, "docker://"):
		spec.Kind = KindDocker
		return spec, nil
	}

	at := strings.LastIndex(value, "@")
	if at <= 0 || at == len(value)-1 {
		spec.Kind = KindUnsupported
		return spec, fmt.Errorf("missing @ref suffix")
	}

	path := value[:at]
	ref := value[at+1:]
	segments := strings.Split(path, "/")
	if len(segments) < 2 {
		spec.Kind = KindUnsupported
		return spec, fmt.Errorf("action path must contain owner/repo")
	}

	spec.Kind = KindRemote
	spec.Repo = strings.Join(segments[:2], "/")
	spec.Ref = ref
	return spec, nil
}

func IsCommitSHA(ref string) bool {
	return shaPattern.MatchString(ref)
}

func ParseMajor(ref string) (int, error) {
	version, err := ParseStableVersion(ref)
	if err != nil {
		return 0, err
	}
	return version.Major, nil
}

type StableVersion struct {
	Original string
	Major    int
	Minor    int
	Patch    int
	HasMinor bool
	HasPatch bool
}

func ParseStableVersion(ref string) (StableVersion, error) {
	if strings.Contains(ref, "-") {
		return StableVersion{}, fmt.Errorf("pre-release refs are unsupported")
	}
	matches := versionPattern.FindStringSubmatch(ref)
	if matches == nil {
		return StableVersion{}, fmt.Errorf("ref is not a supported semver tag")
	}
	version := StableVersion{Original: ref}
	var err error
	if version.Major, err = parseInt(matches[1]); err != nil {
		return StableVersion{}, err
	}
	if matches[2] != "" {
		version.HasMinor = true
		if version.Minor, err = parseInt(matches[2]); err != nil {
			return StableVersion{}, err
		}
	}
	if matches[3] != "" {
		version.HasPatch = true
		if version.Patch, err = parseInt(matches[3]); err != nil {
			return StableVersion{}, err
		}
	}
	return version, nil
}

func parseInt(value string) (int, error) {
	var out int
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", value)
		}
		out = out*10 + int(r-'0')
	}
	return out, nil
}
