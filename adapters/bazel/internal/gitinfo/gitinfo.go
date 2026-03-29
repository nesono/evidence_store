package gitinfo

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type Info struct {
	Repo   string // e.g., "myorg/firmware"
	Branch string // e.g., "main"
	Ref    string // e.g., "abc123def..."
}

// Detect auto-detects git repo, branch, and HEAD ref.
func Detect() (*Info, error) {
	ref, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("detect ref: %w", err)
	}

	branch, err := detectBranch()
	if err != nil {
		return nil, fmt.Errorf("detect branch: %w", err)
	}

	repo, err := detectRepo()
	if err != nil {
		return nil, fmt.Errorf("detect repo: %w", err)
	}

	return &Info{Repo: repo, Branch: branch, Ref: ref}, nil
}

// DetectSource determines the source field: CI build URL or local username.
func DetectSource() string {
	// Jenkins
	if v := os.Getenv("BUILD_URL"); v != "" {
		return v
	}
	// GitLab CI
	if v := os.Getenv("CI_JOB_URL"); v != "" {
		return v
	}
	// GitHub Actions
	if server := os.Getenv("GITHUB_SERVER_URL"); server != "" {
		repo := os.Getenv("GITHUB_REPOSITORY")
		runID := os.Getenv("GITHUB_RUN_ID")
		if repo != "" && runID != "" {
			return fmt.Sprintf("%s/%s/actions/runs/%s", server, repo, runID)
		}
	}
	// Local dev: OS username
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "unknown"
}

func detectBranch() (string, error) {
	branch, err := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if branch != "HEAD" {
		return branch, nil
	}

	// Detached HEAD — check CI env vars.
	for _, env := range []string{"GIT_BRANCH", "BRANCH_NAME", "GITHUB_REF_NAME", "CI_COMMIT_BRANCH"} {
		if v := os.Getenv(env); v != "" {
			return v, nil
		}
	}

	return "HEAD", nil
}

var repoPattern = regexp.MustCompile(`[:/]([^/:]+/[^/.]+?)(?:\.git)?$`)

func detectRepo() (string, error) {
	url, err := gitOutput("remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	matches := repoPattern.FindStringSubmatch(url)
	if len(matches) < 2 {
		return "", fmt.Errorf("cannot parse repo from remote URL: %s", url)
	}
	return matches[1], nil
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}
