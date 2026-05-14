package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"actupdate/internal/actionspec"
	gh "actupdate/internal/github"
	"actupdate/internal/plan"
	"actupdate/internal/workflows"
	"golang.org/x/term"
)

const version = "0.1.0"

const (
	exitOK = iota
	exitOperationalError
	exitInvalidInput
	exitVerificationFailure
)

type cliOptions struct {
	Repo         string
	Yes          bool
	GitHubToken  string
	CooldownDays int
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, http.DefaultClient, gh.DefaultBaseURL))
}

func run(args []string, in io.Reader, out, errOut io.Writer, httpClient *http.Client, githubBaseURL string) int {
	opts, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return exitInvalidInput
	}
	if opts == nil {
		fmt.Fprintln(out, version)
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

	workflowFiles, err := workflows.Discover(repoRoot)
	if err != nil {
		fmt.Fprintf(errOut, "failed to discover workflow files: %v\n", err)
		return exitInvalidInput
	}
	if len(workflowFiles) == 0 {
		fmt.Fprintf(out, "No workflow files found under %s\n", filepath.Join(repoRoot, ".github", "workflows"))
		return exitOK
	}

	scans, err := workflows.ScanFiles(repoRoot, workflowFiles)
	if err != nil {
		fmt.Fprintf(errOut, "failed to scan workflow files: %v\n", err)
		return exitInvalidInput
	}

	client := gh.NewClient(httpClient, githubBaseURL, resolveToken(opts.GitHubToken))
	report, changes, hadVerificationFailure, err := buildReport(context.Background(), scans, client, time.Duration(opts.CooldownDays)*24*time.Hour)
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
		var invalidErr *workflows.InvalidWorkflowError
		if errors.As(err, &invalidErr) {
			fmt.Fprintf(errOut, "invalid rewritten workflow: %v\n", err)
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

	fs := flag.NewFlagSet("actupdate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := &cliOptions{}
	fs.StringVar(&opts.Repo, "repo", "", "path to repository root")
	fs.BoolVar(&opts.Yes, "yes", false, "apply without prompting")
	fs.StringVar(&opts.GitHubToken, "github-token", "", "GitHub token override")
	fs.IntVar(&opts.CooldownDays, "cooldown-days", 0, "minimum tag age in days before upgrading")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.CooldownDays < 0 {
		return nil, fmt.Errorf("--cooldown-days must be non-negative")
	}
	return opts, nil
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
	repoResults := map[string]repoOutcome{}
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

			currentMajor, err := actionspec.ParseMajor(spec.Ref)
			if err != nil {
				entry.Status = plan.StatusSkipped
				entry.Reason = "non-semver ref"
				report.Add(entry)
				continue
			}

			outcome, ok := repoResults[spec.Repo]
			if !ok {
				resolution, resolveErr := client.ResolveLatestMajor(ctx, spec.Repo, currentMajor, cooldown)
				outcome = repoOutcome{Resolution: resolution, Err: resolveErr}
				repoResults[spec.Repo] = outcome
			}

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
