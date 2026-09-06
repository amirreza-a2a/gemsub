// Package publisher generates canonical and protocol-specific subscription
// files along with metadata from the currently servable candidates, and publishes
// them to a local Git repository.
package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
)

// Publication file names.
const (
	FileAll    = "all.txt"
	FileVLESS  = "vless.txt"
	FileVMess  = "vmess.txt"
	FileTrojan = "trojan.txt"
	FileMeta   = "meta.json"
)

var targetFiles = []string{FileAll, FileVLESS, FileVMess, FileTrojan, FileMeta}

// Metadata contains non-secret summary information about the published cycle.
type Metadata struct {
	GeneratedAt   string `json:"generated_at"`
	CycleNumber   int    `json:"cycle_number"`
	TotalServable int    `json:"total_servable"`
	VLESSCount    int    `json:"vless_count"`
	VMessCount    int    `json:"vmess_count"`
	TrojanCount   int    `json:"trojan_count"`
}

// GitRunner abstracts command execution for Git operations so tests can mock Git.
type GitRunner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

type execGitRunner struct{}

func (e *execGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// ExpandHome expands leading ~ or ~/ to the user's home directory.
func ExpandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	} else if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// Publisher coordinates rendering subscription files and committing/pushing them.
type Publisher struct {
	cfg *config.PublishingConfig
	st  *store.Store
	git GitRunner
}

// New creates a new Publisher with default exec-based GitRunner.
func New(cfg *config.PublishingConfig, st *store.Store) *Publisher {
	return NewWithGit(cfg, st, &execGitRunner{})
}

// NewWithGit creates a Publisher with a custom GitRunner (useful for tests).
func NewWithGit(cfg *config.PublishingConfig, st *store.Store, git GitRunner) *Publisher {
	return &Publisher{
		cfg: cfg,
		st:  st,
		git: git,
	}
}

// RenderSubscriptionFiles returns rendered contents of all.txt, vless.txt, vmess.txt, trojan.txt
// and metadata counts based on current servable candidates.
func (p *Publisher) RenderSubscriptionFiles() (map[string][]byte, Metadata) {
	// Deduplicate servable links
	rawLinks := p.st.Passing()
	seen := make(map[string]struct{}, len(rawLinks))
	var unique []string
	for _, link := range rawLinks {
		link = strings.TrimSpace(link)
		if link == "" {
			continue
		}
		if _, exists := seen[link]; !exists {
			seen[link] = struct{}{}
			unique = append(unique, link)
		}
	}
	sort.Strings(unique)

	var vlessLinks, vmessLinks, trojanLinks, otherLinks []string
	for _, link := range unique {
		switch {
		case strings.HasPrefix(link, "vless://"):
			vlessLinks = append(vlessLinks, link)
		case strings.HasPrefix(link, "vmess://"):
			vmessLinks = append(vmessLinks, link)
		case strings.HasPrefix(link, "trojan://"):
			trojanLinks = append(trojanLinks, link)
		default:
			otherLinks = append(otherLinks, link)
		}
	}

	if len(otherLinks) > 0 {
		slog.Warn("publisher: notice: servable candidate(s) with other protocols excluded from protocol-specific files", "count", len(otherLinks))
	}

	stats := p.st.Stats()
	genAt := stats.LastCycle
	if genAt.IsZero() {
		genAt = time.Now()
	}

	meta := Metadata{
		GeneratedAt:   genAt.UTC().Format(time.RFC3339),
		CycleNumber:   stats.CycleCount,
		TotalServable: len(unique),
		VLESSCount:    len(vlessLinks),
		VMessCount:    len(vmessLinks),
		TrojanCount:   len(trojanLinks),
	}

	files := map[string][]byte{
		FileAll:    renderList(unique),
		FileVLESS:  renderList(vlessLinks),
		FileVMess:  renderList(vmessLinks),
		FileTrojan: renderList(trojanLinks),
	}

	return files, meta
}

// GenerateFiles renders all publication files deterministically from current servable candidates.
func (p *Publisher) GenerateFiles() (map[string][]byte, error) {
	files, meta := p.RenderSubscriptionFiles()
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal meta.json: %w", err)
	}
	metaJSON = append(metaJSON, '\n')
	files[FileMeta] = metaJSON
	return files, nil
}

func renderList(links []string) []byte {
	if len(links) == 0 {
		return []byte("")
	}
	return []byte(strings.Join(links, "\n") + "\n")
}

// checkWorkingTreeSafety inspects the Git repository to ensure none of the publisher-owned
// files have pre-existing uncommitted (staged, unstaged, or untracked) modifications.
// This protects manual edits and prevents publishing unintended state.
func (p *Publisher) checkWorkingTreeSafety(ctx context.Context, repoDir string) error {
	statusArgs := append([]string{"status", "--porcelain", "--"}, targetFiles...)
	statusOut, err := p.git.Run(ctx, repoDir, statusArgs...)
	if err != nil {
		return fmt.Errorf("publisher: git status check failed: %w", err)
	}
	if trimmed := strings.TrimSpace(statusOut); trimmed != "" {
		return fmt.Errorf("publisher: working tree has pre-existing uncommitted changes in publisher-owned files:\n%s", trimmed)
	}
	return nil
}

// isAheadOfRemote checks if the local HEAD has unpushed commits relative to origin/<branch>.
func (p *Publisher) isAheadOfRemote(ctx context.Context, repoDir, branch string) (bool, error) {
	// First check if HEAD has any commits
	if _, err := p.git.Run(ctx, repoDir, "rev-parse", "--verify", "HEAD"); err != nil {
		// No commits in local repository yet
		return false, nil
	}

	remoteRef := fmt.Sprintf("origin/%s", branch)
	// Check if the remote tracking ref exists
	if _, err := p.git.Run(ctx, repoDir, "rev-parse", "--verify", remoteRef); err != nil {
		// Remote tracking ref does not exist locally yet; local commits have not been pushed
		return true, nil
	}

	out, err := p.git.Run(ctx, repoDir, "rev-list", "--left-right", "--count", fmt.Sprintf("%s...HEAD", remoteRef))
	if err != nil {
		return false, fmt.Errorf("publisher: rev-list check failed: %w", err)
	}

	fields := strings.Fields(out)
	if len(fields) < 2 {
		return false, fmt.Errorf("publisher: unexpected rev-list output %q", out)
	}

	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return false, fmt.Errorf("publisher: parse rev-list ahead count %q: %w", fields[1], err)
	}

	return ahead > 0, nil
}

// Publish generates subscription files, stages them in the Git repository,
// and pushes them if changes or unpushed commits are detected.
func (p *Publisher) Publish(ctx context.Context) error {
	if p.cfg == nil || !p.cfg.Enabled {
		return nil
	}

	repoDir := ExpandHome(p.cfg.Repository)
	if repoDir == "" {
		return fmt.Errorf("publisher: repository path is empty")
	}

	info, err := os.Stat(repoDir)
	if err != nil {
		return fmt.Errorf("publisher: repository path %q inaccessible: %w", repoDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("publisher: repository path %q is not a directory", repoDir)
	}

	// Verify Git repository
	isGit, err := p.git.Run(ctx, repoDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(isGit) != "true" {
		return fmt.Errorf("publisher: %q is not a valid git repository: %w", repoDir, err)
	}

	// Verify remote origin URL
	originURL, err := p.git.Run(ctx, repoDir, "config", "--get", "remote.origin.url")
	if err != nil {
		originURL, err = p.git.Run(ctx, repoDir, "remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("publisher: failed to get remote origin URL: %w", err)
		}
	}
	originURL = strings.TrimSpace(originURL)

	expectedRemote := strings.TrimSpace(p.cfg.RemoteURL)
	if expectedRemote == "" {
		return fmt.Errorf("publisher: remote_url is required")
	}
	if originURL != expectedRemote {
		return fmt.Errorf("publisher: remote origin URL mismatch: expected %q, got %q", expectedRemote, originURL)
	}

	// Safety check: ensure publisher-owned files have no pre-existing uncommitted changes
	if err := p.checkWorkingTreeSafety(ctx, repoDir); err != nil {
		return err
	}

	slog.Info("publisher: generating subscription files")
	files, meta := p.RenderSubscriptionFiles()

	// Check if all subscription files on disk already match the newly rendered ones.
	// If the subscription content has not changed and existing meta.json is valid with matching counts,
	// preserve the existing meta.json byte content so that timestamp differences do not trigger a commit.
	subscriptionUnchanged := true
	for _, name := range []string{FileAll, FileVLESS, FileVMess, FileTrojan} {
		existing, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil || string(existing) != string(files[name]) {
			subscriptionUnchanged = false
			break
		}
	}

	var metaJSON []byte
	if subscriptionUnchanged {
		existingMeta, err := os.ReadFile(filepath.Join(repoDir, FileMeta))
		if err == nil {
			var existingParsed Metadata
			if json.Unmarshal(existingMeta, &existingParsed) == nil &&
				existingParsed.TotalServable == meta.TotalServable &&
				existingParsed.VLESSCount == meta.VLESSCount &&
				existingParsed.VMessCount == meta.VMessCount &&
				existingParsed.TrojanCount == meta.TrojanCount {
				// Preserve existing meta.json
				metaJSON = existingMeta
			}
		}
	}

	if metaJSON == nil {
		var err error
		metaJSON, err = json.MarshalIndent(meta, "", "  ")
		if err != nil {
			return fmt.Errorf("publisher: marshal meta.json: %w", err)
		}
		metaJSON = append(metaJSON, '\n')
	}
	files[FileMeta] = metaJSON

	// Write generated files to repository directory
	for name, content := range files {
		filePath := filepath.Join(repoDir, name)
		if err := os.WriteFile(filePath, content, 0o644); err != nil {
			return fmt.Errorf("publisher: write %s: %w", name, err)
		}
	}

	// Stage only the five generated files
	addArgs := append([]string{"add"}, targetFiles...)
	if _, err := p.git.Run(ctx, repoDir, addArgs...); err != nil {
		return fmt.Errorf("publisher: git add failed: %w", err)
	}

	// Check if there are any staged changes
	diffArgs := append([]string{"diff", "--cached", "--name-only", "--"}, targetFiles...)
	diffOut, err := p.git.Run(ctx, repoDir, diffArgs...)
	if err != nil {
		return fmt.Errorf("publisher: git diff check failed: %w", err)
	}

	branch := p.cfg.Branch
	if branch == "" {
		branch = "main"
	}

	hasStagedChanges := strings.TrimSpace(diffOut) != ""

	if hasStagedChanges {
		// Commit staged changes
		slog.Info("publisher: committing publication update")
		commitMsg := "chore: update generated subscriptions"
		if _, err := p.git.Run(ctx, repoDir, "commit", "-m", commitMsg); err != nil {
			return fmt.Errorf("publisher: git commit failed: %w", err)
		}

		// Push to branch
		slog.Info("publisher: pushing to origin", "branch", branch)
		if _, err := p.git.Run(ctx, repoDir, "push", "origin", branch); err != nil {
			return fmt.Errorf("publisher: git push failed: %w", err)
		}

		slog.Info("publisher: publish completed")
		return nil
	}

	// No staged changes. Check if local branch is ahead of remote (e.g. from previous failed push).
	ahead, err := p.isAheadOfRemote(ctx, repoDir, branch)
	if err != nil {
		return err
	}

	if !ahead {
		slog.Info("publisher: no changes")
		return nil
	}

	// Local branch has unpushed publication commit(s); retry push without creating another commit
	slog.Info("publisher: unpushed commits detected", "branch", branch)
	if _, err := p.git.Run(ctx, repoDir, "push", "origin", branch); err != nil {
		return fmt.Errorf("publisher: git push failed: %w", err)
	}

	slog.Info("publisher: publish completed")
	return nil
}
