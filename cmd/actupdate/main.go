package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"actupdate/internal/actionspec"
	gh "actupdate/internal/github"
	"actupdate/internal/plan"
	"actupdate/internal/workflows"
	"golang.org/x/term"
)

var version string

const (
	exitOK = iota
	exitOperationalError
	exitInvalidInput
	exitVerificationFailure
)

const maxCooldownDays = int64(math.MaxInt64 / int64(24*time.Hour))
const shortRevisionLength = 12

type cliOptions struct {
	Repo                    string
	Yes                     bool
	GitHubToken             string
	CooldownDays            int
	IncludeCompositeActions bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, http.DefaultClient, gh.DefaultBaseURL, ""))
}

func run(args []string, in io.Reader, out, errOut io.Writer, httpClient *http.Client, githubBaseURL, cacheDir string) int {
	opts, err := parseArgs(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(out)
			return exitOK
		}
		fmt.Fprintln(errOut, err)
		return exitInvalidInput
	}
	if opts == nil {
		fmt.Fprintln(out, displayVersion())
		return exitOK
	}

	repoRoot := opts.Repo
	if repoRoot == "" {
		repoRoot, err = os.Getwd()
		if err != nil {
			fmt.Fprintf(errOut, "failed to resolve current directory: %v\n", err)
			return exitOperationalError
		}
	}
	repoRoot, err = filepath.Abs(repoRoot)
	if err != nil {
		fmt.Fprintf(errOut, "failed to resolve repo path: %v\n", err)
		return exitOperationalError
	}

	discoverOpts := workflows.DiscoverOptions{
		IncludeCompositeActions: opts.IncludeCompositeActions,
	}
	sourceFiles, err := workflows.Discover(repoRoot, discoverOpts)
	if err != nil {
		fmt.Fprintf(errOut, "failed to discover %s: %v\n", discoverTargetLabel(opts.IncludeCompositeActions), err)
		return exitInvalidInput
	}
	if len(sourceFiles) == 0 {
		fmt.Fprintf(out, "No %s found in %s\n", discoverTargetLabel(opts.IncludeCompositeActions), repoRoot)
		return exitOK
	}

	scans, err := workflows.ScanFiles(repoRoot, sourceFiles)
	if err != nil {
		fmt.Fprintf(errOut, "failed to scan %s: %v\n", discoverTargetLabel(opts.IncludeCompositeActions), err)
		return exitInvalidInput
	}

	cooldown, err := cooldownDuration(opts.CooldownDays)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitInvalidInput
	}

	client := gh.NewClient(httpClient, githubBaseURL, resolveToken(opts.GitHubToken))
	if cacheDir != "" {
		client.WithCacheDir(cacheDir)
	}
	report, changes, hadVerificationFailure, err := buildReport(context.Background(), scans, client, cooldown)
	if err != nil {
		fmt.Fprintf(errOut, "failed to build update plan: %v\n", err)
		return exitOperationalError
	}

	fmt.Fprint(out, plan.Render(report, plan.RenderOptions{Color: useColor(out)}))

	if hadVerificationFailure {
		return exitVerificationFailure
	}
	if report.Counts.Updates == 0 {
		return exitOK
	}

	if !opts.Yes {
		confirmed, promptErr := promptConfirm(in, out)
		if promptErr != nil {
			fmt.Fprintf(errOut, "failed to read confirmation: %v\n", promptErr)
			return exitOperationalError
		}
		if !confirmed {
			fmt.Fprintln(out, "Aborted.")
			return exitOK
		}
	}

	if err := workflows.Apply(repoRoot, changes); err != nil {
		var invalidErr *workflows.InvalidYAMLError
		if errors.As(err, &invalidErr) {
			fmt.Fprintf(errOut, "invalid rewritten YAML: %v\n", err)
			return exitInvalidInput
		}
		fmt.Fprintf(errOut, "failed to apply changes: %v\n", err)
		return exitOperationalError
	}

	fmt.Fprintln(out, "Applied updates successfully.")
	return exitOK
}

func parseArgs(args []string) (*cliOptions, error) {
	if len(args) > 0 && args[0] == "version" {
		if len(args) > 1 {
			return nil, fmt.Errorf("version does not accept additional arguments")
		}
		return nil, nil
	}

	fs, opts := newFlagSet(io.Discard)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.CooldownDays < 0 {
		return nil, fmt.Errorf("--cooldown-days must be non-negative")
	}
	if int64(opts.CooldownDays) > maxCooldownDays {
		return nil, fmt.Errorf("--cooldown-days must be at most %d", maxCooldownDays)
	}
	return opts, nil
}

func newFlagSet(out io.Writer) (*flag.FlagSet, *cliOptions) {
	fs := flag.NewFlagSet("actupdate", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(out, `Usage of actupdate:

Updates GitHub Action references in .github/workflows/*.yml and *.yaml files
to the latest eligible stable version. Use --include-composite-actions to also
scan nested action.yml and action.yaml composite actions.

Commands:
  actupdate [flags]
  actupdate version

Flags:
`)
		fs.PrintDefaults()
	}
	opts := &cliOptions{}
	fs.StringVar(&opts.Repo, "repo", "", "path to repository root")
	fs.BoolVar(&opts.Yes, "yes", false, "apply without prompting")
	fs.StringVar(&opts.GitHubToken, "github-token", "", "GitHub token override")
	fs.IntVar(&opts.CooldownDays, "cooldown-days", 0, "minimum tag age in days before upgrading")
	fs.BoolVar(&opts.IncludeCompositeActions, "include-composite-actions", false, "scan nested action.yml and action.yaml composite actions")
	return fs, opts
}

func discoverTargetLabel(includeCompositeActions bool) string {
	if includeCompositeActions {
		return "workflow or composite action files"
	}
	return "workflow files"
}

func printUsage(out io.Writer) {
	fs, _ := newFlagSet(out)
	fs.Usage()
}

func cooldownDuration(days int) (time.Duration, error) {
	if days < 0 {
		return 0, fmt.Errorf("--cooldown-days must be non-negative")
	}
	if int64(days) > maxCooldownDays {
		return 0, fmt.Errorf("--cooldown-days must be at most %d", maxCooldownDays)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func resolveToken(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		return token
	}
	return os.Getenv("GH_TOKEN")
}

func displayVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if buildVersion := versionFromBuildInfo(info); buildVersion != "" {
			return buildVersion
		}
	}
	return "unknown"
}

func versionFromBuildInfo(info *debug.BuildInfo) string {
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}

	var revision string
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	if revision == "" {
		return ""
	}
	if len(revision) > shortRevisionLength {
		revision = revision[:shortRevisionLength]
	}
	out := "devel-" + revision
	if modified {
		out += "+dirty"
	}
	return out
}

func promptConfirm(in io.Reader, out io.Writer) (bool, error) {
	fmt.Fprint(out, "Apply these updates? [Y/n]: ")
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return true, nil
	}
	return line == "y" || line == "yes", nil
}

func useColor(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(file.Fd()))
}

func buildReport(ctx context.Context, scans []workflows.FileScan, client *gh.Client, cooldown time.Duration) (plan.Report, []workflows.Change, bool, error) {
	report := plan.Report{}
	var changes []workflows.Change
	hadVerificationFailure := false

	for _, scan := range scans {
		for _, match := range scan.Matches {
			entry := plan.Entry{
				FilePath: match.FilePath,
				Line:     match.Line,
				Display:  match.Value,
			}

			spec, err := actionspec.Parse(match.Value)
			if err != nil {
				entry.Status = plan.StatusSkipped
				entry.Reason = "malformed action reference"
				report.Add(entry)
				continue
			}

			switch spec.Kind {
			case actionspec.KindLocal:
				entry.Status = plan.StatusSkipped
				entry.Reason = "local action"
				report.Add(entry)
				continue
			case actionspec.KindDocker:
				entry.Status = plan.StatusSkipped
				entry.Reason = "docker reference"
				report.Add(entry)
				continue
			case actionspec.KindUnsupported:
				entry.Status = plan.StatusSkipped
				entry.Reason = "unsupported action reference"
				report.Add(entry)
				continue
			}

			if actionspec.IsCommitSHA(spec.Ref) {
				entry.Status = plan.StatusSkipped
				entry.Reason = "commit SHA pin"
				report.Add(entry)
				continue
			}

			currentVersion, err := actionspec.ParseStableVersion(spec.Ref)
			if err != nil {
				entry.Status = plan.StatusSkipped
				entry.Reason = "non-semver ref"
				report.Add(entry)
				continue
			}

			resolution, resolveErr := client.ResolveLatestStable(ctx, spec.Repo, currentVersion, cooldown)
			outcome := repoOutcome{Resolution: resolution, Err: resolveErr}

			if outcome.Err != nil {
				entry.Status = plan.StatusError
				entry.Reason = outcome.Err.Error()
				report.Add(entry)
				hadVerificationFailure = true
				continue
			}

			if !outcome.Resolution.HasUpgrade {
				entry.Status = plan.StatusUnchanged
				entry.Reason = outcome.Resolution.Reason
				report.Add(entry)
				continue
			}

			entry.Status = plan.StatusUpdate
			entry.NewRef = outcome.Resolution.TargetRef
			entry.Reason = outcome.Resolution.Reason
			report.Add(entry)
			changes = append(changes, workflows.Change{
				Match:  match,
				NewRef: outcome.Resolution.TargetRef,
			})
		}
	}

	return report, changes, hadVerificationFailure, nil
}

type repoOutcome struct {
	Resolution gh.Resolution
	Err        error
}
