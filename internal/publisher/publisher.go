// Package publisher generates canonical and protocol-specific subscription
// files along with metadata from the currently servable candidates, and publishes
// them to a local Git repository.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/store"
)

// Publication directory and file names.
const (
	DirGeneric = "generic"
	DirGemini  = "gemini"

	FileAll    = "all.txt"
	FileVLESS  = "vless.txt"
	FileVMess  = "vmess.txt"
	FileTrojan = "trojan.txt"
	FileMeta   = "meta.json"

	FileGenericAll    = "generic/all.txt"
	FileGenericVLESS  = "generic/vless.txt"
	FileGenericVMess  = "generic/vmess.txt"
	FileGenericTrojan = "generic/trojan.txt"

	FileGeminiAll    = "gemini/all.txt"
	FileGeminiVLESS  = "gemini/vless.txt"
	FileGeminiVMess  = "gemini/vmess.txt"
	FileGeminiTrojan = "gemini/trojan.txt"
)

var targetFiles = []string{
	FileGenericAll,
	FileGenericVLESS,
	FileGenericVMess,
	FileGenericTrojan,
	FileGeminiAll,
	FileGeminiVLESS,
	FileGeminiVMess,
	FileGeminiTrojan,
	FileMeta,
}

var subscriptionFiles = []string{
	FileGenericAll,
	FileGenericVLESS,
	FileGenericVMess,
	FileGenericTrojan,
	FileGeminiAll,
	FileGeminiVLESS,
	FileGeminiVMess,
	FileGeminiTrojan,
}

var legacyRootFiles = []string{
	FileAll,
	FileVLESS,
	FileVMess,
	FileTrojan,
}

// Metadata contains non-secret summary information about the published cycle.
type Metadata struct {
	GeneratedAt string `json:"generated_at"`
	CycleNumber int    `json:"cycle_number"`

	GenericTotal  int `json:"generic_total"`
	GenericVLESS  int `json:"generic_vless"`
	GenericVMess  int `json:"generic_vmess"`
	GenericTrojan int `json:"generic_trojan"`

	GeminiTotal  int `json:"gemini_total"`
	GeminiVLESS  int `json:"gemini_vless"`
	GeminiVMess  int `json:"gemini_vmess"`
	GeminiTrojan int `json:"gemini_trojan"`

	// Legacy fields mapped to Gemini projection for backward compatibility.
	TotalServable int `json:"total_servable"`
	VLESSCount    int `json:"vless_count"`
	VMessCount    int `json:"vmess_count"`
	TrojanCount   int `json:"trojan_count"`
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
	mu                  sync.RWMutex
	cfg                 config.PublishingConfig
	st                  *store.Store
	git                 GitRunner
	lastPublishedCommit string

	writeFileFn  func(targetPath string, content []byte, perm os.FileMode) error
	removeFileFn func(path string) error
}

// New creates a new Publisher with default exec-based GitRunner.
func New(cfg *config.PublishingConfig, st *store.Store) *Publisher {
	return NewWithGit(cfg, st, &execGitRunner{})
}

// NewWithGit creates a Publisher with a custom GitRunner (useful for tests).
func NewWithGit(cfg *config.PublishingConfig, st *store.Store, git GitRunner) *Publisher {
	var c config.PublishingConfig
	if cfg != nil {
		c = *cfg
	}
	return &Publisher{
		cfg: c,
		st:  st,
		git: git,
	}
}

// UpdateConfig updates the publisher configuration under lock.
func (p *Publisher) UpdateConfig(cfg config.PublishingConfig) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg = cfg
}

// Config returns the current publishing configuration snapshot.
func (p *Publisher) Config() config.PublishingConfig {
	if p == nil {
		return config.PublishingConfig{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg
}

// WithFSOverrides configures custom filesystem write and remove hooks for testing failure injection.
func (p *Publisher) WithFSOverrides(writeFn func(path string, content []byte, perm os.FileMode) error, removeFn func(path string) error) *Publisher {
	p.writeFileFn = writeFn
	p.removeFileFn = removeFn
	return p
}

// AtomicWriteFile writes content to a temporary sibling file in targetPath's directory,
// synchronizes it to disk (Sync), closes it, and atomically renames it over targetPath.
// If any step fails, the temporary file is removed.
//
// POSIX rename(2) atomicity guarantees:
// 1. Sibling temp file resides on the same filesystem mount, avoiding cross-device link errors (EXDEV).
// 2. Renaming atomically replaces the destination file without truncation (eliminating the O_TRUNC window).
// 3. Direct filesystem readers never observe empty or partially written files.
func AtomicWriteFile(targetPath string, content []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tempPath := targetPath + ".tmp"
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("create temp file %s: %w", tempPath, err)
	}

	writeErr := func() error {
		if _, err := f.Write(content); err != nil {
			return fmt.Errorf("write temp file %s: %w", tempPath, err)
		}
		if err := f.Sync(); err != nil {
			return fmt.Errorf("sync temp file %s: %w", tempPath, err)
		}
		return nil
	}()

	closeErr := f.Close()
	if writeErr != nil {
		_ = os.Remove(tempPath)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close temp file %s: %w", tempPath, closeErr)
	}

	if err := os.Rename(tempPath, targetPath); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("rename %s to %s: %w", tempPath, targetPath, err)
	}

	return nil
}

func (p *Publisher) atomicWriteFile(targetPath string, content []byte, perm os.FileMode) error {
	if p.writeFileFn != nil {
		return p.writeFileFn(targetPath, content, perm)
	}
	return AtomicWriteFile(targetPath, content, perm)
}

func (p *Publisher) removeFile(path string) error {
	if p.removeFileFn != nil {
		return p.removeFileFn(path)
	}
	return os.Remove(path)
}

func (p *Publisher) hasHEAD(ctx context.Context, repoDir string) bool {
	_, err := p.git.Run(ctx, repoDir, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// LastPublishedCommit returns the exact commit SHA captured from the most recent successful publication.
func (p *Publisher) LastPublishedCommit() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.lastPublishedCommit
}

// LastCommit returns the short commit hash of HEAD if available.
func (p *Publisher) LastCommit(ctx context.Context) string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	cfg := p.cfg
	p.mu.RUnlock()
	repoDir := ExpandHome(cfg.Repository)
	if strings.TrimSpace(repoDir) == "" {
		return ""
	}
	out, err := p.git.Run(ctx, repoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

type pathSnapshot struct {
	existedBefore bool
}

// rollback restores publisher-owned files to their pre-transaction state if publication fails.
// It is strictly scoped to targetFiles and legacyRootFiles; unrelated files are never touched.
func (p *Publisher) rollback(ctx context.Context, repoDir string, snapshot map[string]pathSnapshot, hasHEAD bool) error {
	slog.Warn("publisher: rolling back uncommitted publication changes", "repo", repoDir)
	var errs []error

	// 1. Clean up any lingering sibling temporary files for target files.
	for _, target := range targetFiles {
		tempPath := filepath.Join(repoDir, target+".tmp")
		if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove temp file %s: %w", tempPath, err))
		}
	}

	// 2. Separate publisher-owned files into files to restore vs files to remove.
	allPublisherFiles := append(append([]string{}, targetFiles...), legacyRootFiles...)
	var toRestore []string
	var toRemove []string

	for _, relPath := range allPublisherFiles {
		snap, ok := snapshot[relPath]
		if ok && snap.existedBefore {
			toRestore = append(toRestore, relPath)
		} else {
			toRemove = append(toRemove, relPath)
		}
	}

	// 3. For newly created files (did not exist before transaction):
	// Unstage from Git index and remove from working tree.
	if len(toRemove) > 0 {
		if hasHEAD {
			resetArgs := append([]string{"reset", "HEAD", "--"}, toRemove...)
			if _, err := p.git.Run(ctx, repoDir, resetArgs...); err != nil {
				errs = append(errs, fmt.Errorf("git reset newly created files: %w", err))
			}
		} else {
			rmArgs := append([]string{"rm", "--cached", "-f", "--"}, toRemove...)
			_, _ = p.git.Run(ctx, repoDir, rmArgs...)
		}

		for _, relPath := range toRemove {
			fullPath := filepath.Join(repoDir, relPath)
			if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
				errs = append(errs, fmt.Errorf("remove uncommitted publisher file %s: %w", relPath, err))
			}
		}

		// Clean up empty projection directories if created.
		_ = os.Remove(filepath.Join(repoDir, DirGeneric))
		_ = os.Remove(filepath.Join(repoDir, DirGemini))
	}

	// 4. For files that existed before publication:
	// Restore index and working tree to pre-transaction HEAD state.
	if len(toRestore) > 0 && hasHEAD {
		checkoutArgs := append([]string{"checkout", "HEAD", "--"}, toRestore...)
		if _, err := p.git.Run(ctx, repoDir, checkoutArgs...); err != nil {
			errs = append(errs, fmt.Errorf("git checkout HEAD restored files: %w", err))
		}
	}

	return errors.Join(errs...)
}

// partitionLinks deduplicates, sorts, and partitions raw candidate links by protocol scheme.
//
// Ordering policy rationale:
// Both Store.NetworkPassing() and Store.Passing() provide candidate links in stable sorted
// alphabetical order. We preserve deterministic alphabetical ordering within each publication
// file (generic/* and gemini/*).
// Using latency or reliability-ranked ordering (e.g. NetworkPassingRanked or PassingRanked)
// would introduce line-ordering churn on every cycle due to slight network jitter even when
// the set of passing candidates has not changed. Alphabetical sorting guarantees byte-for-byte
// identical outputs across cycles with unchanged candidate sets, preventing spurious Git commits.
func partitionLinks(rawLinks []string, projectionName string) (all, vless, vmess, trojan []string) {
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
		slog.Warn("publisher: notice: candidate(s) with other protocols excluded from protocol-specific files",
			"projection", projectionName,
			"count", len(otherLinks),
		)
	}

	return unique, vlessLinks, vmessLinks, trojanLinks
}

// RenderSubscriptionFiles returns rendered contents of generic/* and gemini/* subscription files
// and metadata counts based on current Store projections.
func (p *Publisher) RenderSubscriptionFiles() (map[string][]byte, Metadata) {
	// 1. Generic projection: sourced directly from Store.NetworkPassing()
	genAll, genVLESS, genVMess, genTrojan := partitionLinks(p.st.NetworkPassing(), DirGeneric)

	// 2. Gemini projection: sourced directly from Store.Passing()
	gemAll, gemVLESS, gemVMess, gemTrojan := partitionLinks(p.st.Passing(), DirGemini)

	stats := p.st.Stats()
	genAt := stats.LastCycle
	if genAt.IsZero() {
		genAt = time.Now()
	}

	meta := Metadata{
		GeneratedAt:   genAt.UTC().Format(time.RFC3339),
		CycleNumber:   stats.CycleCount,
		GenericTotal:  len(genAll),
		GenericVLESS:  len(genVLESS),
		GenericVMess:  len(genVMess),
		GenericTrojan: len(genTrojan),
		GeminiTotal:   len(gemAll),
		GeminiVLESS:   len(gemVLESS),
		GeminiVMess:   len(gemVMess),
		GeminiTrojan:  len(gemTrojan),

		// Legacy fields mapped to Gemini projection for backward compatibility
		TotalServable: len(gemAll),
		VLESSCount:    len(gemVLESS),
		VMessCount:    len(gemVMess),
		TrojanCount:   len(gemTrojan),
	}

	files := map[string][]byte{
		FileGenericAll:    renderList(genAll),
		FileGenericVLESS:  renderList(genVLESS),
		FileGenericVMess:  renderList(genVMess),
		FileGenericTrojan: renderList(genTrojan),

		FileGeminiAll:    renderList(gemAll),
		FileGeminiVLESS:  renderList(gemVLESS),
		FileGeminiVMess:  renderList(gemVMess),
		FileGeminiTrojan: renderList(gemTrojan),
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
	safetyFiles := append([]string{}, targetFiles...)
	for _, legacy := range legacyRootFiles {
		legacyPath := filepath.Join(repoDir, legacy)
		if _, err := os.Lstat(legacyPath); err == nil {
			safetyFiles = append(safetyFiles, legacy)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("publisher: lstat safety check %s: %w", legacy, err)
		}
	}
	statusArgs := append([]string{"status", "--porcelain", "--"}, safetyFiles...)
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

// ValidatePrerequisites verifies the real minimum prerequisites needed before publication:
// - context cancellation
// - publishing is enabled
// - repository path exists and is a directory
// - repository is actually a git repository
// - configured remote URL matches origin
// - branch is configured
// It does not commit, push, or mutate repository state.
func (p *Publisher) ValidatePrerequisites(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	p.mu.RLock()
	cfg := p.cfg
	p.mu.RUnlock()

	if !cfg.Enabled {
		return ErrPublishingDisabled
	}

	repoDir := ExpandHome(cfg.Repository)
	if strings.TrimSpace(repoDir) == "" {
		return fmt.Errorf("%w: publisher: repository path is empty", ErrInvalidConfiguration)
	}

	info, err := os.Stat(repoDir)
	if err != nil {
		return fmt.Errorf("%w: publisher: repository path %q inaccessible: %v", ErrRepositoryUnavailable, repoDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: publisher: repository path %q is not a directory", ErrRepositoryUnavailable, repoDir)
	}

	// Verify Git repository
	isGit, err := p.git.Run(ctx, repoDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || strings.TrimSpace(isGit) != "true" {
		return fmt.Errorf("%w: publisher: %q is not a valid git repository", ErrRepositoryUnavailable, repoDir)
	}

	expectedRemote := strings.TrimSpace(cfg.RemoteURL)
	if expectedRemote == "" {
		return fmt.Errorf("%w: publisher: remote_url is required", ErrInvalidConfiguration)
	}

	// Verify remote origin URL
	originURL, err := p.git.Run(ctx, repoDir, "config", "--get", "remote.origin.url")
	if err != nil {
		originURL, err = p.git.Run(ctx, repoDir, "remote", "get-url", "origin")
		if err != nil {
			return fmt.Errorf("%w: publisher: failed to get remote origin URL: %v", ErrInvalidConfiguration, err)
		}
	}
	originURL = strings.TrimSpace(originURL)

	if !SameRepository(originURL, expectedRemote) {
		return fmt.Errorf("%w: publisher: remote origin URL mismatch: expected %q, got %q", ErrInvalidConfiguration, SanitizeURL(expectedRemote), SanitizeURL(originURL))
	}

	return nil
}

// Publish generates subscription files, stages them in the Git repository,
// and pushes them if changes or unpushed commits are detected.
func (p *Publisher) Publish(ctx context.Context) (err error) {
	if err := p.ValidatePrerequisites(ctx); err != nil {
		if errors.Is(err, ErrPublishingDisabled) {
			return nil
		}
		return err
	}

	p.mu.RLock()
	cfg := p.cfg
	p.mu.RUnlock()
	repoDir := ExpandHome(cfg.Repository)

	// Safety check: ensure publisher-owned files have no pre-existing uncommitted changes
	if err := p.checkWorkingTreeSafety(ctx, repoDir); err != nil {
		return err
	}

	// Transaction boundary: capture pre-mutation existence state of publisher-owned files.
	// Any mid-transaction failure triggers automated rollback scoped strictly to publisher files.
	hasHEAD := p.hasHEAD(ctx, repoDir)
	snapshot := make(map[string]pathSnapshot, len(targetFiles)+len(legacyRootFiles))
	for _, relPath := range append(append([]string{}, targetFiles...), legacyRootFiles...) {
		fullPath := filepath.Join(repoDir, relPath)
		_, lstatErr := os.Lstat(fullPath)
		snapshot[relPath] = pathSnapshot{existedBefore: lstatErr == nil}
	}

	committedOrClean := false
	defer func() {
		if !committedOrClean {
			rbErr := p.rollback(ctx, repoDir, snapshot, hasHEAD)
			if rbErr != nil {
				if err != nil {
					err = errors.Join(err, fmt.Errorf("publisher: rollback failed: %w", rbErr))
				} else {
					err = fmt.Errorf("publisher: rollback failed: %w", rbErr)
				}
			}
		}
	}()

	slog.Info("publisher: generating subscription files")
	files, meta := p.RenderSubscriptionFiles()

	// Check if all subscription files on disk already match the newly rendered ones,
	// and ensure no deprecated legacy root-level subscription files exist.
	// If the subscription content has not changed and existing meta.json is valid with matching counts,
	// preserve the existing meta.json byte content so that timestamp differences do not trigger a commit.
	subscriptionUnchanged := true
	for _, name := range subscriptionFiles {
		existing, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil || string(existing) != string(files[name]) {
			subscriptionUnchanged = false
			break
		}
	}
	if subscriptionUnchanged {
		for _, legacy := range legacyRootFiles {
			legacyPath := filepath.Join(repoDir, legacy)
			if _, err := os.Lstat(legacyPath); err == nil {
				subscriptionUnchanged = false
				break
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("publisher: check legacy file %s: %w", legacy, err)
			}
		}
	}

	var metaJSON []byte
	if subscriptionUnchanged {
		existingMeta, err := os.ReadFile(filepath.Join(repoDir, FileMeta))
		if err == nil {
			var existingParsed Metadata
			if json.Unmarshal(existingMeta, &existingParsed) == nil &&
				existingParsed.GenericTotal == meta.GenericTotal &&
				existingParsed.GenericVLESS == meta.GenericVLESS &&
				existingParsed.GenericVMess == meta.GenericVMess &&
				existingParsed.GenericTrojan == meta.GenericTrojan &&
				existingParsed.GeminiTotal == meta.GeminiTotal &&
				existingParsed.GeminiVLESS == meta.GeminiVLESS &&
				existingParsed.GeminiVMess == meta.GeminiVMess &&
				existingParsed.GeminiTrojan == meta.GeminiTrojan &&
				existingParsed.TotalServable == meta.GeminiTotal &&
				existingParsed.VLESSCount == meta.GeminiVLESS &&
				existingParsed.VMessCount == meta.GeminiVMess &&
				existingParsed.TrojanCount == meta.GeminiTrojan {
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

	// Remove any legacy root-level subscription files so they do not linger and get mistaken for current output
	var removedLegacyFiles []string
	for _, legacy := range legacyRootFiles {
		legacyPath := filepath.Join(repoDir, legacy)
		_, err := os.Lstat(legacyPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("publisher: lstat legacy file %s: %w", legacy, err)
		}
		if err := p.removeFile(legacyPath); err != nil {
			return fmt.Errorf("publisher: remove legacy file %s: %w", legacy, err)
		}
		removedLegacyFiles = append(removedLegacyFiles, legacy)
	}

	// Write generated files to repository directory using atomic sibling temporary files
	for _, name := range targetFiles {
		content, ok := files[name]
		if !ok {
			continue
		}
		filePath := filepath.Join(repoDir, name)
		if err := p.atomicWriteFile(filePath, content, 0o644); err != nil {
			return fmt.Errorf("publisher: write %s: %w", name, err)
		}
	}

	// Stage all generated dual-projection files and meta.json, plus any removed legacy root files
	stageFiles := append([]string{}, targetFiles...)
	stageFiles = append(stageFiles, removedLegacyFiles...)
	addArgs := append([]string{"add", "--"}, stageFiles...)
	if _, err := p.git.Run(ctx, repoDir, addArgs...); err != nil {
		return fmt.Errorf("publisher: git add failed: %w", err)
	}

	// Check if there are any staged changes
	diffArgs := append([]string{"diff", "--cached", "--name-only", "--"}, stageFiles...)
	diffOut, err := p.git.Run(ctx, repoDir, diffArgs...)
	if err != nil {
		return fmt.Errorf("publisher: git diff check failed: %w", err)
	}

	branch := cfg.Branch
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

		committedOrClean = true

		// Capture exact commit SHA belonging to this publication before push
		shaOut, err := p.git.Run(ctx, repoDir, "rev-parse", "--short", "HEAD")
		if err != nil {
			return fmt.Errorf("publisher: capture commit SHA: %w", err)
		}
		publishedSHA := strings.TrimSpace(shaOut)

		// Push to branch
		slog.Info("publisher: pushing to origin", "branch", branch)
		if _, err := p.git.Run(ctx, repoDir, "push", "origin", branch); err != nil {
			return fmt.Errorf("publisher: git push failed: %w", err)
		}

		p.mu.Lock()
		p.lastPublishedCommit = publishedSHA
		p.mu.Unlock()

		slog.Info("publisher: publish completed")
		return nil
	}

	committedOrClean = true

	// No staged changes. Check if local branch is ahead of remote (e.g. from previous failed push).
	ahead, err := p.isAheadOfRemote(ctx, repoDir, branch)
	if err != nil {
		return err
	}

	if !ahead {
		slog.Info("publisher: no changes")
		return nil
	}

	// Capture exact commit SHA belonging to unpushed publication before push
	shaOut, err := p.git.Run(ctx, repoDir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return fmt.Errorf("publisher: capture commit SHA: %w", err)
	}
	publishedSHA := strings.TrimSpace(shaOut)

	// Local branch has unpushed publication commit(s); retry push without creating another commit
	slog.Info("publisher: unpushed commits detected", "branch", branch)
	if _, err := p.git.Run(ctx, repoDir, "push", "origin", branch); err != nil {
		return fmt.Errorf("publisher: git push failed: %w", err)
	}

	p.mu.Lock()
	p.lastPublishedCommit = publishedSHA
	p.mu.Unlock()

	slog.Info("publisher: publish completed")
	return nil
}
