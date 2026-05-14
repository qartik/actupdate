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
6. Show the planned updates with colorized terminal output when supported
7. Prompt once before rewriting files; pressing Enter accepts the default and
   applies the changes

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
actupdate --cooldown-days 7
actupdate --github-token "$GITHUB_TOKEN"
actupdate version
```

Flags:

- `--repo`: operate on a different repo root instead of the current directory
- `--yes`: apply immediately after printing the plan
- `--cooldown-days`: ignore candidate tags newer than the given number of days
- `--github-token`: override token lookup; otherwise the tool uses
  `GITHUB_TOKEN`, then `GH_TOKEN`

Use `--cooldown-days` when you want to avoid immediately adopting freshly
published action tags. For example, `actupdate --cooldown-days 7` only upgrades
to tags that are at least seven days old.

## GitHub Auth

If you see `verification failed: ... GitHub API rate limited or forbidden`,
`actupdate` is usually running without a GitHub token or has exhausted the
token's rate limit.

If you already use `gh`, you can pass its token directly:

```bash
GITHUB_TOKEN="$(gh auth token)" actupdate
```

## Releases

Push a tag like `v0.1.0` to trigger the release workflow. It builds and uploads
tarballs for:

- `linux/amd64`
- `linux/arm64`
- `darwin/arm64`

Each release asset is named like `actupdate_linux_amd64.tar.gz` and contains the
`actupdate` binary.

## Verification Rules

- Only stable semver tags are considered
- Pre-release tags such as `-rc`, `-beta`, and `-alpha` are ignored
- Updates only move to the latest stable major
- `--cooldown-days` can exclude newer tags until they have aged past the
  configured threshold
- Same-major patch or minor bumps are not applied in v1
- If any candidate update cannot be verified, the tool prints the failures and
  does not rewrite any files

## Development

Run tests with:

```bash
go test ./...
```
