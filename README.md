# actupdate

`actupdate` updates GitHub Action references in workflow YAML files to the latest
stable major version.

## What It Does

Run `actupdate` inside a git repo and it will:

1. Scan `.github/workflows/*.yml` and `.github/workflows/*.yaml`
2. Find remote `uses:` references such as `actions/checkout@v4`
3. Query GitHub for the action repository's tags
4. Prefer a moving major tag such as `v6` when it exists
5. Fall back to the latest verified stable exact tag such as `v6.2.1` when the
   repo does not publish moving major tags
6. Show the planned updates and prompt once before rewriting files

The tool skips local actions, Docker references, SHA pins, branch refs, and
other non-semver references.

## Usage

```bash
actupdate
```

Options:

```bash
actupdate --repo /path/to/repo
actupdate --yes
actupdate --github-token "$GITHUB_TOKEN"
actupdate version
```

Flags:

- `--repo`: operate on a different repo root instead of the current directory
- `--yes`: apply immediately after printing the plan
- `--github-token`: override token lookup; otherwise the tool uses
  `GITHUB_TOKEN`, then `GH_TOKEN`

## Verification Rules

- Only stable semver tags are considered
- Pre-release tags such as `-rc`, `-beta`, and `-alpha` are ignored
- Updates only move to the latest stable major
- Same-major patch or minor bumps are not applied in v1
- If any candidate update cannot be verified, the tool prints the failures and
  does not rewrite any files

## Development

Run tests with:

```bash
go test ./...
```
