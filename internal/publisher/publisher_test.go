package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gemsub/internal/config"
	"gemsub/internal/publisher"
	"gemsub/internal/store"
)

type mockGitRunner struct {
	calls   [][]string
	runFunc func(ctx context.Context, dir string, args ...string) (string, error)
}

func (m *mockGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	m.calls = append(m.calls, args)
	if m.runFunc != nil {
		return m.runFunc(ctx, dir, args...)
	}
	return "", nil
}

func TestPublisher_DeterministicOutputAndDuplicateRemoval(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Add candidates in non-sorted order with TRUE DUPLICATES
	links := []string{
		"vmess://xyz",
		"vless://bbb",
		"trojan://ccc",
		"vless://aaa",
		"vmess://xyz", // duplicate
		"vless://aaa", // duplicate
		"vmess://abc",
	}
	for _, link := range links {
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			TestedAt: time.Now(),
		})
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
	}
	pub := publisher.New(cfg, st)

	files1, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles failed: %v", err)
	}
	files2, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles 2nd run failed: %v", err)
	}

	// Determinism check: bytes must be identical
	for k, v1 := range files1 {
		v2, ok := files2[k]
		if !ok {
			t.Fatalf("missing file %s in files2", k)
		}
		if string(v1) != string(v2) {
			t.Fatalf("file %s non-deterministic: \nrun1: %s\nrun2: %s", k, string(v1), string(v2))
		}
	}

	// Expected sorted order and duplicate removal in both projections
	geminiAll := string(files1[publisher.FileGeminiAll])
	genericAll := string(files1[publisher.FileGenericAll])
	expectedAll := "trojan://ccc\nvless://aaa\nvless://bbb\nvmess://abc\nvmess://xyz\n"
	if geminiAll != expectedAll {
		t.Fatalf("gemini/all.txt mismatch:\nexpected:\n%s\ngot:\n%s", expectedAll, geminiAll)
	}
	if genericAll != expectedAll {
		t.Fatalf("generic/all.txt mismatch:\nexpected:\n%s\ngot:\n%s", expectedAll, genericAll)
	}

	// Verify exact count of duplicates in all.txt
	if strings.Count(geminiAll, "vless://aaa") != 1 {
		t.Errorf("expected vless://aaa to appear exactly once, got count %d", strings.Count(geminiAll, "vless://aaa"))
	}
	if strings.Count(geminiAll, "vmess://xyz") != 1 {
		t.Errorf("expected vmess://xyz to appear exactly once, got count %d", strings.Count(geminiAll, "vmess://xyz"))
	}
}

func TestPublisher_OnlyServableCandidatesPublished(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// 1. Passing link
	st.PutWithTransition(store.Result{
		Link:     "vless://passed",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})

	// 2. Failed link
	st.PutWithTransition(store.Result{
		Link:     "vless://failed",
		Status:   store.StatusFailed,
		TestedAt: time.Now(),
	})

	// 3. Inconclusive with prior pass (retains last-known-good)
	st.PutWithTransition(store.Result{
		Link:     "vmess://inconclusive-lkg",
		Status:   store.StatusPassed,
		TestedAt: time.Now(),
	})
	st.PutWithTransition(store.Result{
		Link:     "vmess://inconclusive-lkg",
		Status:   store.StatusInconclusive,
		TestedAt: time.Now(),
	})

	// 4. Inconclusive fresh candidate (not servable)
	st.PutWithTransition(store.Result{
		Link:     "trojan://inconclusive-fresh",
		Status:   store.StatusInconclusive,
		TestedAt: time.Now(),
	})

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
	}
	pub := publisher.New(cfg, st)

	files, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles failed: %v", err)
	}

	all := string(files[publisher.FileGeminiAll])
	if !strings.Contains(all, "vless://passed") {
		t.Errorf("expected vless://passed to be in gemini/all.txt")
	}
	if !strings.Contains(all, "vmess://inconclusive-lkg") {
		t.Errorf("expected vmess://inconclusive-lkg to be in gemini/all.txt (last-known-good)")
	}
	if strings.Contains(all, "vless://failed") {
		t.Errorf("vless://failed must NOT be in gemini/all.txt")
	}
	if strings.Contains(all, "trojan://inconclusive-fresh") {
		t.Errorf("trojan://inconclusive-fresh must NOT be in gemini/all.txt")
	}
}

func TestPublisher_ProtocolGrouping(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	links := []string{
		"vless://vless-1",
		"vless://vless-2",
		"vmess://vmess-1",
		"trojan://trojan-1",
		"ss://shadowsocks-1", // other protocol
	}
	for _, link := range links {
		st.PutWithTransition(store.Result{
			Link:     link,
			Status:   store.StatusPassed,
			TestedAt: time.Now(),
		})
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
	}
	pub := publisher.New(cfg, st)

	files, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles failed: %v", err)
	}

	vless := string(files[publisher.FileGeminiVLESS])
	expectedVless := "vless://vless-1\nvless://vless-2\n"
	if vless != expectedVless {
		t.Errorf("gemini/vless.txt mismatch: expected %q, got %q", expectedVless, vless)
	}

	vmess := string(files[publisher.FileGeminiVMess])
	expectedVmess := "vmess://vmess-1\n"
	if vmess != expectedVmess {
		t.Errorf("gemini/vmess.txt mismatch: expected %q, got %q", expectedVmess, vmess)
	}

	trojan := string(files[publisher.FileGeminiTrojan])
	expectedTrojan := "trojan://trojan-1\n"
	if trojan != expectedTrojan {
		t.Errorf("gemini/trojan.txt mismatch: expected %q, got %q", expectedTrojan, trojan)
	}

	all := string(files[publisher.FileGeminiAll])
	if !strings.Contains(all, "ss://shadowsocks-1") {
		t.Errorf("gemini/all.txt should include other servable protocols (ss://)")
	}
}

func TestPublisher_Metadata(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	st.PutWithTransition(store.Result{Link: "vless://1", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "vless://2", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "vmess://1", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "trojan://1", Status: store.StatusPassed})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
	}
	pub := publisher.New(cfg, st)

	files, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles failed: %v", err)
	}

	var meta publisher.Metadata
	if err := json.Unmarshal(files[publisher.FileMeta], &meta); err != nil {
		t.Fatalf("unmarshal meta.json failed: %v", err)
	}

	if meta.CycleNumber != 1 {
		t.Errorf("expected CycleNumber=1, got %d", meta.CycleNumber)
	}
	if meta.TotalServable != 4 {
		t.Errorf("expected TotalServable=4, got %d", meta.TotalServable)
	}
	if meta.GeminiTotal != 4 {
		t.Errorf("expected GeminiTotal=4, got %d", meta.GeminiTotal)
	}
	if meta.GenericTotal != 4 {
		t.Errorf("expected GenericTotal=4, got %d", meta.GenericTotal)
	}
	if meta.VLESSCount != 2 || meta.GeminiVLESS != 2 || meta.GenericVLESS != 2 {
		t.Errorf("expected VLESS counts = 2, got VLESSCount=%d, GeminiVLESS=%d, GenericVLESS=%d",
			meta.VLESSCount, meta.GeminiVLESS, meta.GenericVLESS)
	}
	if meta.VMessCount != 1 || meta.GeminiVMess != 1 || meta.GenericVMess != 1 {
		t.Errorf("expected VMess counts = 1, got VMessCount=%d, GeminiVMess=%d, GenericVMess=%d",
			meta.VMessCount, meta.GeminiVMess, meta.GenericVMess)
	}
	if meta.TrojanCount != 1 || meta.GeminiTrojan != 1 || meta.GenericTrojan != 1 {
		t.Errorf("expected Trojan counts = 1, got TrojanCount=%d, GeminiTrojan=%d, GenericTrojan=%d",
			meta.TrojanCount, meta.GeminiTrojan, meta.GenericTrojan)
	}
	if _, err := time.Parse(time.RFC3339, meta.GeneratedAt); err != nil {
		t.Errorf("expected RFC3339 generated_at, got %q", meta.GeneratedAt)
	}
}

func TestPublisher_WorkingTreeSafety(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Pre-existing unstaged changes to generic/all.txt
	mockUnstaged := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:example/gemsub-subscriptions.git\n", nil
				case "status":
					return " M generic/all.txt\n", nil
				}
			}
			return "", nil
		},
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
	}
	pubUnstaged := publisher.NewWithGit(cfg, st, mockUnstaged)
	err := pubUnstaged.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
		t.Fatalf("expected error on pre-existing unstaged changes, got: %v", err)
	}

	// Pre-existing staged changes to gemini/vless.txt
	mockStaged := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:example/gemsub-subscriptions.git\n", nil
				case "status":
					return "M  gemini/vless.txt\n", nil
				}
			}
			return "", nil
		},
	}
	pubStaged := publisher.NewWithGit(cfg, st, mockStaged)
	err = pubStaged.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
		t.Fatalf("expected error on pre-existing staged changes, got: %v", err)
	}
}

// interceptingGitRunner wraps real git execution and optionally fails push commands.
type interceptingGitRunner struct {
	failPush  bool
	pushCalls int32
}

func (r *interceptingGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "push" {
		atomic.AddInt32(&r.pushCalls, 1)
		if r.failPush {
			return "", fmt.Errorf("simulated network push failure")
		}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func TestPublisher_RealGitIntegration_PushFailureRecovery(t *testing.T) {
	// Set up local bare remote to receive pushes without external network
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	// Set up local working repository
	repoDir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	// Create initial README commit so repository has history
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub-subscriptions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial readme")
	runGit("push", "origin", "main")

	// Add an uncommitted unrelated file in the working tree
	notesFile := filepath.Join(repoDir, "notes.txt")
	if err := os.WriteFile(notesFile, []byte("uncommitted user notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://user@1.1.1.1:443", Status: store.StatusPassed})
	st.PutWithTransition(store.Result{Link: "vmess://user@2.2.2.2:443", Status: store.StatusPassed})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	runner := &interceptingGitRunner{failPush: true}
	pub := publisher.NewWithGit(cfg, st, runner)

	// --- Cycle 1: commit succeeds, push fails ---
	err := pub.Publish(context.Background())
	if err == nil {
		t.Fatalf("expected error on failed push, got nil")
	}
	if !strings.Contains(err.Error(), "simulated network push failure") {
		t.Fatalf("expected simulated push error, got: %v", err)
	}

	if atomic.LoadInt32(&runner.pushCalls) != 1 {
		t.Fatalf("expected pushCalls=1 after 1st cycle, got %d", runner.pushCalls)
	}

	// Verify local commit was created
	commitCountCmd := exec.Command("git", "rev-list", "--count", "HEAD")
	commitCountCmd.Dir = repoDir
	commitCountOut, err := commitCountCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// Initial commit + 1 publication commit = 2 commits locally
	if strings.TrimSpace(string(commitCountOut)) != "2" {
		t.Fatalf("expected 2 commits locally, got %s", strings.TrimSpace(string(commitCountOut)))
	}

	head1Cmd := exec.Command("git", "rev-parse", "HEAD")
	head1Cmd.Dir = repoDir
	head1Out, err := head1Cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pubCommitHash := strings.TrimSpace(string(head1Out))

	// Verify bare remote does NOT have the publication commit yet
	remoteHeadCmd := exec.Command("git", "--git-dir="+bareRemoteDir, "rev-parse", "HEAD")
	remoteHeadOut, err := remoteHeadCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(remoteHeadOut)) == pubCommitHash {
		t.Fatalf("bare remote should not have received publication commit during failed push")
	}

	// Verify notes.txt remains untracked
	statusCmd := exec.Command("git", "status", "--porcelain", "--", "notes.txt")
	statusCmd.Dir = repoDir
	statusOut, err := statusCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(statusOut)) != "?? notes.txt" {
		t.Fatalf("expected notes.txt to remain untracked (??), got %q", string(statusOut))
	}

	// --- Cycle 2: push failure recovery with IDENTICAL content ---
	// Network is restored (failPush = false)
	runner.failPush = false
	st.FinishCycle() // new cycle with same results
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("2nd Publish (push retry) failed: %v", err)
	}

	// Assert push was retried
	if atomic.LoadInt32(&runner.pushCalls) != 2 {
		t.Fatalf("expected pushCalls=2 after retry, got %d", runner.pushCalls)
	}

	// Assert NO new commit was created
	commitCountCmd = exec.Command("git", "rev-list", "--count", "HEAD")
	commitCountCmd.Dir = repoDir
	commitCountOut, err = commitCountCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(commitCountOut)) != "2" {
		t.Fatalf("commit count must remain 2 (no duplicate/empty commit), got %s", strings.TrimSpace(string(commitCountOut)))
	}

	head2Cmd := exec.Command("git", "rev-parse", "HEAD")
	head2Cmd.Dir = repoDir
	head2Out, err := head2Cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(head2Out)) != pubCommitHash {
		t.Fatalf("HEAD hash must remain unchanged (%s), got %s", pubCommitHash, strings.TrimSpace(string(head2Out)))
	}

	// Verify bare remote HEAD now contains the original publication commit
	remoteHeadCmd = exec.Command("git", "--git-dir="+bareRemoteDir, "rev-parse", "HEAD")
	remoteHeadOut, err = remoteHeadCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(remoteHeadOut)) != pubCommitHash {
		t.Fatalf("expected bare remote HEAD to match %s, got %s", pubCommitHash, strings.TrimSpace(string(remoteHeadOut)))
	}

	// --- Cycle 3: true no-op when synchronized with identical content ---
	st.FinishCycle()
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("3rd Publish failed: %v", err)
	}

	// Assert push was NOT called again (synchronized no-op)
	if atomic.LoadInt32(&runner.pushCalls) != 2 {
		t.Fatalf("pushCalls must remain 2 on synchronized no-op, got %d", runner.pushCalls)
	}

	// --- Cycle 4: changed content triggers new commit and push ---
	st.StartCycle(map[string]struct{}{
		"vless://user@1.1.1.1:443": {},
	})
	st.FinishCycle()

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("4th Publish failed: %v", err)
	}

	// Assert push was called for new content
	if atomic.LoadInt32(&runner.pushCalls) != 3 {
		t.Fatalf("expected pushCalls=3 after content change, got %d", runner.pushCalls)
	}

	// Verify new commit was created
	commitCountCmd = exec.Command("git", "rev-list", "--count", "HEAD")
	commitCountCmd.Dir = repoDir
	commitCountOut, err = commitCountCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(commitCountOut)) != "3" {
		t.Fatalf("expected 3 commits after content change, got %s", strings.TrimSpace(string(commitCountOut)))
	}

	head3Cmd := exec.Command("git", "rev-parse", "HEAD")
	head3Cmd.Dir = repoDir
	head3Out, err := head3Cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	newCommitHash := strings.TrimSpace(string(head3Out))
	if newCommitHash == pubCommitHash {
		t.Fatalf("expected new commit hash after content change")
	}

	// Verify bare remote received the new commit
	remoteHeadCmd = exec.Command("git", "--git-dir="+bareRemoteDir, "rev-parse", "HEAD")
	remoteHeadOut, err = remoteHeadCmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(remoteHeadOut)) != newCommitHash {
		t.Fatalf("expected bare remote HEAD to match new commit %s, got %s", newCommitHash, strings.TrimSpace(string(remoteHeadOut)))
	}
}

func TestPublisher_RepositoryValidation(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Missing repo directory
	cfgMissing := &config.PublishingConfig{
		Enabled:    true,
		Repository: filepath.Join(tmpDir, "non-existent"),
	}
	pub := publisher.New(cfgMissing, st)
	if err := pub.Publish(context.Background()); err == nil {
		t.Fatalf("expected error for non-existent repository")
	}

	// Not a git repository
	mockNotGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "rev-parse" {
				return "fatal: not a git repository", errors.New("exit status 128")
			}
			return "", nil
		},
	}
	cfgNotGit := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
	}
	pubNotGit := publisher.NewWithGit(cfgNotGit, st, mockNotGit)
	if err := pubNotGit.Publish(context.Background()); err == nil {
		t.Fatalf("expected error for non-git repository")
	}

	// Remote URL mismatch
	mockWrongRemote := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "rev-parse" {
				return "true\n", nil
			}
			if len(args) > 0 && args[0] == "config" {
				return "git@github.com:someone-else/repo.git\n", nil
			}
			return "", nil
		},
	}
	cfgWrongRemote := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
	}
	pubWrongRemote := publisher.NewWithGit(cfgWrongRemote, st, mockWrongRemote)
	if err := pubWrongRemote.Publish(context.Background()); err == nil {
		t.Fatalf("expected error for remote URL mismatch")
	}

	// Empty RemoteURL rejected
	mockValidGit := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 && args[0] == "rev-parse" {
				return "true\n", nil
			}
			if len(args) > 0 && args[0] == "config" {
				return "git@github.com:example/gemsub-subscriptions.git\n", nil
			}
			return "", nil
		},
	}
	cfgEmptyRemote := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "",
	}
	pubEmptyRemote := publisher.NewWithGit(cfgEmptyRemote, st, mockValidGit)
	if err := pubEmptyRemote.Publish(context.Background()); err == nil {
		t.Fatalf("expected error for empty remote URL")
	} else if !strings.Contains(err.Error(), "publisher: remote_url is required") {
		t.Fatalf("expected 'publisher: remote_url is required', got: %v", err)
	}
}

func TestPublisher_ValidatePrerequisites_CanonicalEquivalenceAndSafety(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	tests := []struct {
		name          string
		actualOrigin  string
		configuredURL string
		expectPass    bool
	}{
		{
			name:          "actual SSH origin, configured HTTPS URL -> PASS",
			actualOrigin:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "https://github.com/amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    true,
		},
		{
			name:          "actual HTTPS origin, configured SSH URL -> PASS",
			actualOrigin:  "https://github.com/amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    true,
		},
		{
			name:          "actual SSH URI origin, configured SCP SSH URL -> PASS",
			actualOrigin:  "ssh://git@github.com/amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    true,
		},
		{
			name:          "actual SSH URI with port 22, configured HTTPS URL -> PASS",
			actualOrigin:  "ssh://git@github.com:22/amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "https://github.com/amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    true,
		},
		{
			name:          "different repository path -> FAIL",
			actualOrigin:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "https://github.com/amirreza-a2a/other-repo.git",
			expectPass:    false,
		},
		{
			name:          "different host -> FAIL",
			actualOrigin:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "git@gitlab.com:amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    false,
		},
		{
			name:          "mismatched custom SSH port -> FAIL",
			actualOrigin:  "ssh://git@github.com:2222/amirreza-a2a/gemsub-subscriptions.git",
			configuredURL: "ssh://git@github.com:22/amirreza-a2a/gemsub-subscriptions.git",
			expectPass:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockGitRunner{
				runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					if len(args) > 0 && args[0] == "rev-parse" {
						return "true\n", nil
					}
					if len(args) > 0 && args[0] == "config" {
						return tc.actualOrigin + "\n", nil
					}
					return "", nil
				},
			}
			cfg := &config.PublishingConfig{
				Enabled:    true,
				Repository: tmpDir,
				Branch:     "main",
				RemoteURL:  tc.configuredURL,
			}
			pub := publisher.NewWithGit(cfg, st, mock)
			err := pub.ValidatePrerequisites(context.Background())
			if tc.expectPass && err != nil {
				t.Fatalf("expected ValidatePrerequisites to pass, got: %v", err)
			}
			if !tc.expectPass {
				if err == nil {
					t.Fatalf("expected ValidatePrerequisites to fail on mismatch, got nil")
				}
				if !errors.Is(err, publisher.ErrInvalidConfiguration) {
					t.Fatalf("expected ErrInvalidConfiguration, got: %v", err)
				}
				if !strings.Contains(err.Error(), "remote origin URL mismatch") {
					t.Fatalf("expected error containing 'remote origin URL mismatch', got: %v", err)
				}
			}
		})
	}
}

func TestPublisher_GitFailuresSurfaced(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	tests := []struct {
		name      string
		failOn    string
		errSubstr string
	}{
		{
			name:      "git add fails",
			failOn:    "add",
			errSubstr: "git add failed",
		},
		{
			name:      "git commit fails",
			failOn:    "commit",
			errSubstr: "git commit failed",
		},
		{
			name:      "git push fails",
			failOn:    "push",
			errSubstr: "git push failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &mockGitRunner{
				runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					if len(args) > 0 {
						cmd := args[0]
						if cmd == "rev-parse" {
							return "true\n", nil
						}
						if cmd == "config" {
							return "git@github.com:example/gemsub-subscriptions.git\n", nil
						}
						if cmd == "status" {
							return "", nil
						}
						if cmd == "diff" {
							return "all.txt\n", nil // indicates staged changes exist
						}
						if cmd == tt.failOn {
							return "", fmt.Errorf("mock error for %s", cmd)
						}
					}
					return "", nil
				},
			}

			cfg := &config.PublishingConfig{
				Enabled:    true,
				Repository: tmpDir,
				Branch:     "main",
				RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
			}
			pub := publisher.NewWithGit(cfg, st, mock)
			err := pub.Publish(context.Background())
			if err == nil {
				t.Fatalf("expected error on %s failure, got nil", tt.failOn)
			}
			if !strings.Contains(err.Error(), tt.errSubstr) {
				t.Errorf("expected error containing %q, got %q", tt.errSubstr, err.Error())
			}
		})
	}
}

func TestPublisher_ExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home directory")
	}

	if got := publisher.ExpandHome("~"); got != home {
		t.Errorf("ExpandHome(~) = %q, expected %q", got, home)
	}
	if got := publisher.ExpandHome("~/gemsub-subscriptions"); got != filepath.Join(home, "gemsub-subscriptions") {
		t.Errorf("ExpandHome(~/gemsub-subscriptions) = %q, expected %q", got, filepath.Join(home, "gemsub-subscriptions"))
	}
	if got := publisher.ExpandHome("/var/tmp/repo"); got != "/var/tmp/repo" {
		t.Errorf("ExpandHome(/var/tmp/repo) = %q, expected /var/tmp/repo", got)
	}
}

func TestPublisher_TestA_HealthyTransportAndGeminiPass(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Stage 1 transport pass + Stage 2 Gemini pass
	st.PutWithTransition(store.Result{
		Link:                   "vless://healthy-and-pass",
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	// Appears in BOTH generic and gemini
	genAll := string(files[publisher.FileGenericAll])
	gemAll := string(files[publisher.FileGeminiAll])
	if !strings.Contains(genAll, "vless://healthy-and-pass") {
		t.Errorf("expected candidate in generic/all.txt, got %q", genAll)
	}
	if !strings.Contains(gemAll, "vless://healthy-and-pass") {
		t.Errorf("expected candidate in gemini/all.txt, got %q", gemAll)
	}

	// Also in protocol files
	if !strings.Contains(string(files[publisher.FileGenericVLESS]), "vless://healthy-and-pass") {
		t.Errorf("expected candidate in generic/vless.txt")
	}
	if !strings.Contains(string(files[publisher.FileGeminiVLESS]), "vless://healthy-and-pass") {
		t.Errorf("expected candidate in gemini/vless.txt")
	}

	if meta.GenericTotal != 1 || meta.GenericVLESS != 1 {
		t.Errorf("unexpected generic counts: %+v", meta)
	}
	if meta.GeminiTotal != 1 || meta.GeminiVLESS != 1 || meta.TotalServable != 1 {
		t.Errorf("unexpected gemini counts: %+v", meta)
	}
}

func TestPublisher_TestB_HealthyTransportAndRegionBlocked(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Stage 1 transport pass + Stage 2 Gemini RegionBlocked
	st.PutWithTransition(store.Result{
		Link:                   "vless://region-blocked",
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	// Appears in generic, NOT in gemini
	genAll := string(files[publisher.FileGenericAll])
	gemAll := string(files[publisher.FileGeminiAll])
	if !strings.Contains(genAll, "vless://region-blocked") {
		t.Errorf("expected candidate in generic/all.txt, got %q", genAll)
	}
	if strings.Contains(gemAll, "vless://region-blocked") {
		t.Errorf("candidate must NOT be in gemini/all.txt, got %q", gemAll)
	}

	if !strings.Contains(string(files[publisher.FileGenericVLESS]), "vless://region-blocked") {
		t.Errorf("expected candidate in generic/vless.txt")
	}
	if strings.Contains(string(files[publisher.FileGeminiVLESS]), "vless://region-blocked") {
		t.Errorf("candidate must NOT be in gemini/vless.txt")
	}

	if meta.GenericTotal != 1 || meta.GenericVLESS != 1 {
		t.Errorf("unexpected generic counts: %+v", meta)
	}
	if meta.GeminiTotal != 0 || meta.GeminiVLESS != 0 || meta.TotalServable != 0 {
		t.Errorf("unexpected gemini counts: %+v", meta)
	}
}

func TestPublisher_TestC_HealthyTransportAndTargetDenied(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Stage 1 transport pass + Stage 2 Gemini TargetDenied
	st.PutWithTransition(store.Result{
		Link:                   "vmess://target-denied",
		Status:                 store.StatusFailed,
		Category:               store.ErrTargetDenied,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	// Appears in generic, NOT in gemini
	genAll := string(files[publisher.FileGenericAll])
	gemAll := string(files[publisher.FileGeminiAll])
	if !strings.Contains(genAll, "vmess://target-denied") {
		t.Errorf("expected candidate in generic/all.txt, got %q", genAll)
	}
	if strings.Contains(gemAll, "vmess://target-denied") {
		t.Errorf("candidate must NOT be in gemini/all.txt, got %q", gemAll)
	}

	if !strings.Contains(string(files[publisher.FileGenericVMess]), "vmess://target-denied") {
		t.Errorf("expected candidate in generic/vmess.txt")
	}
	if strings.Contains(string(files[publisher.FileGeminiVMess]), "vmess://target-denied") {
		t.Errorf("candidate must NOT be in gemini/vmess.txt")
	}

	if meta.GenericTotal != 1 || meta.GenericVMess != 1 {
		t.Errorf("unexpected generic counts: %+v", meta)
	}
	if meta.GeminiTotal != 0 || meta.GeminiVLESS != 0 || meta.TotalServable != 0 {
		t.Errorf("unexpected gemini counts: %+v", meta)
	}
}

func TestPublisher_TestD_HealthyTransportAndInconclusive_WithAndWithoutLKG(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// 1. Fresh inconclusive candidate with healthy transport
	st.PutWithTransition(store.Result{
		Link:                   "trojan://inconclusive-fresh",
		Status:                 store.StatusInconclusive,
		Category:               store.ErrTimeout,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})

	// 2. Prior pass candidate followed by inconclusive (LKG) with healthy transport
	st.PutWithTransition(store.Result{
		Link:                   "vless://inconclusive-lkg",
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now().Add(-10 * time.Minute),
	})
	st.PutWithTransition(store.Result{
		Link:                   "vless://inconclusive-lkg",
		Status:                 store.StatusInconclusive,
		Category:               store.ErrTimeout,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})

	// 3. Inconclusive with transport timeout (transport failure)
	st.PutWithTransition(store.Result{
		Link:                   "vmess://transport-timeout",
		Status:                 store.StatusInconclusive,
		Category:               store.ErrTimeout,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               time.Now(),
	})

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	genAll := string(files[publisher.FileGenericAll])
	gemAll := string(files[publisher.FileGeminiAll])

	// Fresh inconclusive (healthy transport): in generic, NOT in gemini
	if !strings.Contains(genAll, "trojan://inconclusive-fresh") {
		t.Errorf("fresh inconclusive with healthy transport should be in generic/all.txt")
	}
	if strings.Contains(gemAll, "trojan://inconclusive-fresh") {
		t.Errorf("fresh inconclusive must NOT be in gemini/all.txt")
	}

	// LKG inconclusive (healthy transport): in generic AND in gemini (retains LKG)
	if !strings.Contains(genAll, "vless://inconclusive-lkg") {
		t.Errorf("LKG candidate should be in generic/all.txt")
	}
	if !strings.Contains(gemAll, "vless://inconclusive-lkg") {
		t.Errorf("LKG candidate must be in gemini/all.txt")
	}

	// Inconclusive with transport failure: in NEITHER generic nor gemini
	if strings.Contains(genAll, "vmess://transport-timeout") {
		t.Errorf("transport timeout candidate must NOT be in generic/all.txt")
	}
	if strings.Contains(gemAll, "vmess://transport-timeout") {
		t.Errorf("transport timeout candidate must NOT be in gemini/all.txt")
	}

	if meta.GenericTotal != 2 {
		t.Errorf("expected GenericTotal=2, got %d", meta.GenericTotal)
	}
	if meta.GeminiTotal != 1 {
		t.Errorf("expected GeminiTotal=1, got %d", meta.GeminiTotal)
	}
}

func TestPublisher_TestE_TransportFailureExcludedFromBoth(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Candidate with Stage 1 transport failure
	st.PutWithTransition(store.Result{
		Link:                   "vless://transport-fail",
		Status:                 store.StatusFailed,
		Category:               store.ErrProxyError,
		TransportEvidenceKnown: true,
		TransportOK:            false,
		TestedAt:               time.Now(),
	})

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	genAll := string(files[publisher.FileGenericAll])
	gemAll := string(files[publisher.FileGeminiAll])

	if strings.Contains(genAll, "vless://transport-fail") {
		t.Errorf("transport failure must NOT be in generic/all.txt")
	}
	if strings.Contains(gemAll, "vless://transport-fail") {
		t.Errorf("transport failure must NOT be in gemini/all.txt")
	}
	if meta.GenericTotal != 0 || meta.GeminiTotal != 0 || meta.TotalServable != 0 {
		t.Errorf("expected 0 counts, got generic=%d, gemini=%d", meta.GenericTotal, meta.GeminiTotal)
	}
}

func TestPublisher_TestF_ProtocolSpecificSeparation(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	candidates := []store.Result{
		{Link: "vless://gem-pass", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "vmess://gem-pass", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "trojan://gem-pass", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "ss://gem-pass", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "vless://gen-blocked", Status: store.StatusFailed, Category: store.ErrRegionBlocked, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "vmess://gen-denied", Status: store.StatusFailed, Category: store.ErrTargetDenied, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "trojan://gen-error", Status: store.StatusFailed, Category: store.ErrTargetError, TransportEvidenceKnown: true, TransportOK: true},
		{Link: "vless://trans-fail", Status: store.StatusFailed, Category: store.ErrProxyError, TransportEvidenceKnown: true, TransportOK: false},
	}
	for _, c := range candidates {
		c.TestedAt = time.Now()
		st.PutWithTransition(c)
	}

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	files, meta := pub.RenderSubscriptionFiles()

	// Generic protocol files:
	genVless := string(files[publisher.FileGenericVLESS])
	expectedGenVless := "vless://gem-pass\nvless://gen-blocked\n"
	if genVless != expectedGenVless {
		t.Errorf("generic/vless.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGenVless, genVless)
	}

	genVmess := string(files[publisher.FileGenericVMess])
	expectedGenVmess := "vmess://gem-pass\nvmess://gen-denied\n"
	if genVmess != expectedGenVmess {
		t.Errorf("generic/vmess.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGenVmess, genVmess)
	}

	genTrojan := string(files[publisher.FileGenericTrojan])
	expectedGenTrojan := "trojan://gem-pass\ntrojan://gen-error\n"
	if genTrojan != expectedGenTrojan {
		t.Errorf("generic/trojan.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGenTrojan, genTrojan)
	}

	genAll := string(files[publisher.FileGenericAll])
	if strings.Contains(genAll, "vless://trans-fail") {
		t.Errorf("generic/all.txt must NOT contain transport failure")
	}
	if !strings.Contains(genAll, "ss://gem-pass") {
		t.Errorf("generic/all.txt must contain unsupported protocol candidate ss://")
	}

	// Gemini protocol files:
	gemVless := string(files[publisher.FileGeminiVLESS])
	expectedGemVless := "vless://gem-pass\n"
	if gemVless != expectedGemVless {
		t.Errorf("gemini/vless.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGemVless, gemVless)
	}

	gemVmess := string(files[publisher.FileGeminiVMess])
	expectedGemVmess := "vmess://gem-pass\n"
	if gemVmess != expectedGemVmess {
		t.Errorf("gemini/vmess.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGemVmess, gemVmess)
	}

	gemTrojan := string(files[publisher.FileGeminiTrojan])
	expectedGemTrojan := "trojan://gem-pass\n"
	if gemTrojan != expectedGemTrojan {
		t.Errorf("gemini/trojan.txt mismatch:\nwant:\n%s\ngot:\n%s", expectedGemTrojan, gemTrojan)
	}

	gemAll := string(files[publisher.FileGeminiAll])
	if !strings.Contains(gemAll, "ss://gem-pass") {
		t.Errorf("gemini/all.txt must contain servable candidate with ss:// scheme")
	}
	if strings.Contains(gemAll, "vless://gen-blocked") || strings.Contains(gemAll, "vmess://gen-denied") || strings.Contains(gemAll, "trojan://gen-error") {
		t.Errorf("gemini/all.txt must NOT contain generic-only candidates")
	}

	// Verify metadata counts
	if meta.GenericTotal != 7 || meta.GenericVLESS != 2 || meta.GenericVMess != 2 || meta.GenericTrojan != 2 {
		t.Errorf("unexpected generic counts: %+v", meta)
	}
	if meta.GeminiTotal != 4 || meta.GeminiVLESS != 1 || meta.GeminiVMess != 1 || meta.GeminiTrojan != 1 || meta.TotalServable != 4 {
		t.Errorf("unexpected gemini counts: %+v", meta)
	}
}

func TestPublisher_TestG_StaleOutputRemovalAndEmptyFileHandling(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Cycle 1:
	// - vless://c1 passes Gemini
	// - trojan://c2 passes Gemini
	// - vmess://c3 is RegionBlocked (Generic only)
	st.PutWithTransition(store.Result{Link: "vless://c1", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.PutWithTransition(store.Result{Link: "trojan://c2", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.PutWithTransition(store.Result{Link: "vmess://c3", Status: store.StatusFailed, Category: store.ErrRegionBlocked, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)
	_, meta1 := pub.RenderSubscriptionFiles()

	if meta1.GeminiTotal != 2 || meta1.GenericTotal != 3 {
		t.Fatalf("cycle 1 counts mismatch: gemini=%d, generic=%d", meta1.GeminiTotal, meta1.GenericTotal)
	}

	// Cycle 2:
	// - vless://c1 suffers complete transport failure -> removed from BOTH generic and gemini
	// - trojan://c2 becomes RegionBlocked -> removed from Gemini, remains in Generic
	// - vmess://c3 remains RegionBlocked -> remains in Generic only
	st.StartCycle(map[string]struct{}{
		"vless://c1":  {},
		"trojan://c2": {},
		"vmess://c3":  {},
	})
	st.PutWithTransition(store.Result{Link: "vless://c1", Status: store.StatusFailed, Category: store.ErrProxyError, TransportEvidenceKnown: true, TransportOK: false})
	st.PutWithTransition(store.Result{Link: "trojan://c2", Status: store.StatusFailed, Category: store.ErrRegionBlocked, TransportEvidenceKnown: true, TransportOK: true})
	st.PutWithTransition(store.Result{Link: "vmess://c3", Status: store.StatusFailed, Category: store.ErrRegionBlocked, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	files2, meta2 := pub.RenderSubscriptionFiles()

	// Gemini projection is now completely empty
	if meta2.GeminiTotal != 0 || meta2.GeminiVLESS != 0 || meta2.GeminiVMess != 0 || meta2.GeminiTrojan != 0 || meta2.TotalServable != 0 {
		t.Errorf("expected 0 gemini servable, got %+v", meta2)
	}
	if len(files2[publisher.FileGeminiAll]) != 0 {
		t.Errorf("expected empty gemini/all.txt, got %q", string(files2[publisher.FileGeminiAll]))
	}
	if len(files2[publisher.FileGeminiVLESS]) != 0 {
		t.Errorf("expected empty gemini/vless.txt, got %q", string(files2[publisher.FileGeminiVLESS]))
	}
	if len(files2[publisher.FileGeminiTrojan]) != 0 {
		t.Errorf("expected empty gemini/trojan.txt, got %q", string(files2[publisher.FileGeminiTrojan]))
	}

	// Generic projection has trojan://c2 and vmess://c3; vless://c1 must be purged
	if meta2.GenericTotal != 2 || meta2.GenericVLESS != 0 || meta2.GenericTrojan != 1 || meta2.GenericVMess != 1 {
		t.Errorf("expected generic total=2 (trojan=1, vmess=1, vless=0), got %+v", meta2)
	}
	if len(files2[publisher.FileGenericVLESS]) != 0 {
		t.Errorf("expected empty generic/vless.txt, got %q", string(files2[publisher.FileGenericVLESS]))
	}
	if strings.Contains(string(files2[publisher.FileGenericAll]), "vless://c1") {
		t.Errorf("stale candidate vless://c1 must not be in generic/all.txt")
	}
	if !strings.Contains(string(files2[publisher.FileGenericAll]), "trojan://c2") ||
		!strings.Contains(string(files2[publisher.FileGenericAll]), "vmess://c3") {
		t.Errorf("generic/all.txt missing active network-healthy candidates: %s", string(files2[publisher.FileGenericAll]))
	}
}

func TestPublisher_TestH_ChangeDetection_IndependentProjections(t *testing.T) {
	// Setup real git bare remote and working repo
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial readme")
	runGit("push", "origin", "main")

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}
	pub := publisher.New(cfg, st)

	// Step 1: Initial commit when Store has 1 Gemini pass candidate
	st.PutWithTransition(store.Result{Link: "vless://pass-1", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("step 1 publish failed: %v", err)
	}
	commit1 := runGit("rev-parse", "HEAD")

	// Step 2: Identical candidates and counts -> no-op, no new commit
	st.FinishCycle()
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("step 2 publish failed: %v", err)
	}
	commit2 := runGit("rev-parse", "HEAD")
	if commit2 != commit1 {
		t.Fatalf("expected no commit on unchanged cycle, got %s != %s", commit2, commit1)
	}

	// Step 3: Generic changes ONLY (RegionBlocked candidate added), Gemini output UNCHANGED
	// Gemini: remains [vless://pass-1]
	// Generic: becomes [vless://pass-1, vmess://blocked-1]
	geminiBeforeStep3, err := os.ReadFile(filepath.Join(repoDir, "gemini", "all.txt"))
	if err != nil {
		t.Fatal(err)
	}

	st.PutWithTransition(store.Result{
		Link:                   "vmess://blocked-1",
		Status:                 store.StatusFailed,
		Category:               store.ErrRegionBlocked,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("step 3 publish failed: %v", err)
	}
	commit3 := runGit("rev-parse", "HEAD")
	if commit3 == commit2 {
		t.Fatalf("expected new commit when generic output changed, but HEAD remained %s", commit3)
	}

	geminiAfterStep3, err := os.ReadFile(filepath.Join(repoDir, "gemini", "all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(geminiBeforeStep3) != string(geminiAfterStep3) {
		t.Fatalf("Gemini output must remain byte-identical in generic-only change")
	}

	// Step 4: Gemini changes ONLY (vmess://blocked-1 transitions from RegionBlocked to PASS)
	// Candidate was already in Generic; its promotion to Gemini leaves Generic bytes unchanged while adding to Gemini.
	genericBeforeStep4, err := os.ReadFile(filepath.Join(repoDir, "generic", "all.txt"))
	if err != nil {
		t.Fatal(err)
	}

	st.PutWithTransition(store.Result{
		Link:                   "vmess://blocked-1",
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.PutWithTransition(store.Result{
		Link:                   "vmess://blocked-1",
		Status:                 store.StatusPassed,
		Category:               store.ErrNone,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("step 4 publish failed: %v", err)
	}
	commit4 := runGit("rev-parse", "HEAD")
	if commit4 == commit3 {
		t.Fatalf("expected new commit when Gemini output changed, but HEAD remained %s", commit4)
	}

	genericAfterStep4, err := os.ReadFile(filepath.Join(repoDir, "generic", "all.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(genericBeforeStep4) != string(genericAfterStep4) {
		t.Fatalf("Generic output must remain byte-identical in Gemini-only change:\nbefore:\n%s\nafter:\n%s",
			string(genericBeforeStep4), string(genericAfterStep4))
	}

	// Step 5: Both projections unchanged, metadata counts identical -> no-op, no new commit
	st.FinishCycle()
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("step 5 publish failed: %v", err)
	}
	commit5 := runGit("rev-parse", "HEAD")
	if commit5 != commit4 {
		t.Fatalf("expected no commit on synchronized identical cycle, got %s != %s", commit5, commit4)
	}
}

func TestPublisher_TestI_WorkingTreeSafety_DualProjections(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	uncommittedFiles := []string{
		"generic/all.txt",
		"generic/vmess.txt",
		"gemini/all.txt",
		"gemini/trojan.txt",
		"meta.json",
	}

	for _, uncommittedFile := range uncommittedFiles {
		t.Run("protect_"+uncommittedFile, func(t *testing.T) {
			mock := &mockGitRunner{
				runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
					if len(args) > 0 {
						switch args[0] {
						case "rev-parse":
							return "true\n", nil
						case "config":
							return "git@github.com:example/gemsub-subscriptions.git\n", nil
						case "status":
							return fmt.Sprintf(" M %s\n", uncommittedFile), nil
						}
					}
					return "", nil
				},
			}

			cfg := &config.PublishingConfig{
				Enabled:    true,
				Repository: tmpDir,
				RemoteURL:  "git@github.com:example/gemsub-subscriptions.git",
			}
			pub := publisher.NewWithGit(cfg, st, mock)
			err := pub.Publish(context.Background())
			if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
				t.Fatalf("expected error for uncommitted %s, got: %v", uncommittedFile, err)
			}
		})
	}
}

func TestPublisher_OrderingPolicy_AlphabeticalDeterminism(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Insert candidates in deliberately reverse alphabetical order
	raw := []string{
		"vless://zzz",
		"vless://mmm",
		"vless://aaa",
	}
	for _, link := range raw {
		st.PutWithTransition(store.Result{
			Link:                   link,
			Status:                 store.StatusPassed,
			TransportEvidenceKnown: true,
			TransportOK:            true,
			TestedAt:               time.Now(),
		})
	}

	cfg := &config.PublishingConfig{Enabled: true, Repository: tmpDir}
	pub := publisher.New(cfg, st)

	files, err := pub.GenerateFiles()
	if err != nil {
		t.Fatalf("GenerateFiles failed: %v", err)
	}

	expected := "vless://aaa\nvless://mmm\nvless://zzz\n"
	if string(files[publisher.FileGenericAll]) != expected {
		t.Errorf("generic/all.txt not sorted alphabetically: got %q", string(files[publisher.FileGenericAll]))
	}
	if string(files[publisher.FileGeminiAll]) != expected {
		t.Errorf("gemini/all.txt not sorted alphabetically: got %q", string(files[publisher.FileGeminiAll]))
	}
	if string(files[publisher.FileGenericVLESS]) != expected {
		t.Errorf("generic/vless.txt not sorted alphabetically: got %q", string(files[publisher.FileGenericVLESS]))
	}
	if string(files[publisher.FileGeminiVLESS]) != expected {
		t.Errorf("gemini/vless.txt not sorted alphabetically: got %q", string(files[publisher.FileGeminiVLESS]))
	}
}

func TestPublisher_TestLegacyRootFileCleanup(t *testing.T) {
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	// Create legacy root files
	legacyFiles := []string{"all.txt", "vless.txt", "vmess.txt", "trojan.txt"}
	for _, f := range legacyFiles {
		if err := os.WriteFile(filepath.Join(repoDir, f), []byte("legacy-candidate\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit("add", ".")
	runGit("commit", "-m", "legacy publication with root files")
	runGit("push", "origin", "main")

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://fresh-pass",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}
	pub := publisher.New(cfg, st)

	// Cycle 1: should clean up legacy root files and write generic/ + gemini/
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Verify legacy root files are removed from disk
	for _, f := range legacyFiles {
		p := filepath.Join(repoDir, f)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected legacy root file %s to be deleted from disk, but it still exists", f)
		}
	}

	// Verify dual projection files exist on disk
	if _, err := os.Stat(filepath.Join(repoDir, "generic", "all.txt")); err != nil {
		t.Errorf("generic/all.txt missing on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "gemini", "all.txt")); err != nil {
		t.Errorf("gemini/all.txt missing on disk: %v", err)
	}

	// Verify Git repository tree has dual projections and no legacy files
	treeListing := runGit("ls-tree", "-r", "--name-only", "HEAD")
	for _, f := range legacyFiles {
		for _, line := range strings.Split(treeListing, "\n") {
			if line == f {
				t.Errorf("legacy root file %s still present in Git tree: %s", f, treeListing)
			}
		}
	}
	if !strings.Contains(treeListing, "generic/all.txt") || !strings.Contains(treeListing, "gemini/all.txt") {
		t.Errorf("tree listing missing dual projection files: %s", treeListing)
	}

	// Cycle 2: identical candidates -> synchronized no-op (no new commit)
	headBefore := runGit("rev-parse", "HEAD")
	st.FinishCycle()
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("Publish cycle 2 failed: %v", err)
	}
	headAfter := runGit("rev-parse", "HEAD")
	if headBefore != headAfter {
		t.Errorf("expected no commit on cycle 2, got %s != %s", headBefore, headAfter)
	}
}

func TestPublisher_WorkingTreeSafety_PolicyA_UntrackedLegacyFileAborts(t *testing.T) {
	// Setup real git bare remote and working repo
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial readme")
	runGit("push", "origin", "main")

	// Add an unrelated untracked file (e.g. notes.txt)
	notesPath := filepath.Join(repoDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("manual notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Add an untracked legacy root file (e.g. all.txt)
	legacyPath := filepath.Join(repoDir, "all.txt")
	if err := os.WriteFile(legacyPath, []byte("unexpected manual file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://test", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}
	pub := publisher.New(cfg, st)

	// Policy A: Untracked legacy file must trigger working tree safety abort!
	err := pub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
		t.Fatalf("expected working tree safety error for untracked legacy file, got: %v", err)
	}

	// Verify all.txt was NOT deleted
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		t.Errorf("untracked legacy file was deleted; Policy A requires protecting manual/unexpected files")
	}

	// Verify unrelated notes.txt was NOT deleted
	if _, err := os.Stat(notesPath); os.IsNotExist(err) {
		t.Errorf("unrelated notes.txt was deleted")
	}

	// Verify no commit was created
	commitCount := runGit("rev-list", "--count", "HEAD")
	if commitCount != "1" {
		t.Errorf("commit count must remain 1, got %s", commitCount)
	}
}

func TestPublisher_LegacyFileRemoval_ErrorFailsLoudly(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://test", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	// Create a non-removable legacy file directory
	legacyDir := filepath.Join(tmpDir, "all.txt")
	if err := os.Mkdir(legacyDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a non-empty child inside all.txt directory so os.Remove fails
	if err := os.WriteFile(filepath.Join(legacyDir, "child.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	mock := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:example/repo.git\n", nil
				case "status":
					return "", nil // working tree passes
				}
			}
			return "", nil
		},
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "git@github.com:example/repo.git",
	}
	pub := publisher.NewWithGit(cfg, st, mock)

	err := pub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "remove legacy file all.txt") {
		t.Fatalf("expected loud failure removing legacy file, got: %v", err)
	}
}

func TestPublisher_MetadataPreservation_RecomputesOnInconsistentLegacyCounts(t *testing.T) {
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial readme")
	runGit("push", "origin", "main")

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://pass-1", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}
	pub := publisher.New(cfg, st)

	// Cycle 1: initial publication
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("cycle 1 failed: %v", err)
	}

	// Corrupt meta.json on disk so that generic_* and gemini_* match, but legacy total_servable is inconsistent
	metaPath := filepath.Join(repoDir, "meta.json")
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var metaObj publisher.Metadata
	if err := json.Unmarshal(metaBytes, &metaObj); err != nil {
		t.Fatal(err)
	}
	// Invalidate legacy count
	metaObj.TotalServable = 999
	corruptedBytes, _ := json.MarshalIndent(metaObj, "", "  ")
	corruptedBytes = append(corruptedBytes, '\n')
	if err := os.WriteFile(metaPath, corruptedBytes, 0644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "meta.json")
	runGit("commit", "-m", "manually commit corrupted legacy metadata")
	runGit("push", "origin", "main")

	// Next cycle: subscription content is unchanged, but meta.json legacy fields are inconsistent
	st.FinishCycle()
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("Publish after corrupted metadata failed: %v", err)
	}

	// Verify meta.json was recomputed to restore total_servable == gemini_total
	updatedMetaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var updatedMeta publisher.Metadata
	if err := json.Unmarshal(updatedMetaBytes, &updatedMeta); err != nil {
		t.Fatal(err)
	}
	if updatedMeta.TotalServable != updatedMeta.GeminiTotal {
		t.Errorf("expected total_servable (%d) == gemini_total (%d)", updatedMeta.TotalServable, updatedMeta.GeminiTotal)
	}
	if updatedMeta.TotalServable != 1 {
		t.Errorf("expected total_servable == 1, got %d", updatedMeta.TotalServable)
	}
}

// --- Ticket 26: Atomic publication and transactional rollback tests ---

func setupTestGitRepo(t *testing.T) (bareRemoteDir, repoDir string, runGit func(args ...string) string) {
	t.Helper()
	bareRemoteDir = t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare failed: %v: %s", err, out)
	}

	repoDir = t.TempDir()
	runGit = func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial readme")
	runGit("push", "origin", "main")

	return bareRemoteDir, repoDir, runGit
}

type commandFailingGitRunner struct {
	failOnCmd string
	failOnce  bool
	failed    bool
}

func (r *commandFailingGitRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == r.failOnCmd {
		if !r.failed || !r.failOnce {
			r.failed = true
			return "", fmt.Errorf("simulated git %s failure", r.failOnCmd)
		}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func TestPublisher_AtomicWriteFile_Guarantees(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "sub", "target.txt")

	// 1. Initial write creates directory and file atomically
	content1 := []byte("first content version\n")
	if err := publisher.AtomicWriteFile(targetFile, content1, 0o644); err != nil {
		t.Fatalf("AtomicWriteFile failed: %v", err)
	}

	read1, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read targetFile: %v", err)
	}
	if string(read1) != string(content1) {
		t.Fatalf("expected content %q, got %q", string(content1), string(read1))
	}

	// Sibling temp file must not remain
	if _, err := os.Stat(targetFile + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temporary sibling file still exists after success")
	}

	// 2. Overwrite replaces existing file
	content2 := []byte("second updated content\n")
	if err := publisher.AtomicWriteFile(targetFile, content2, 0o644); err != nil {
		t.Fatalf("AtomicWriteFile overwrite failed: %v", err)
	}

	read2, err := os.ReadFile(targetFile)
	if err != nil {
		t.Fatalf("read targetFile after overwrite: %v", err)
	}
	if string(read2) != string(content2) {
		t.Fatalf("expected content %q, got %q", string(content2), string(read2))
	}

	// 3. Failed write cleans up temporary file
	// If targetPath is an existing directory, os.Rename(tempPath, targetPath) fails (EISDIR),
	// triggering the cleanup of tempPath.
	targetDir := filepath.Join(tmpDir, "existing_dir")
	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err = publisher.AtomicWriteFile(targetDir, []byte("data"), 0o644)
	if err == nil {
		t.Fatalf("expected error writing file over directory")
	}
	if _, statErr := os.Stat(targetDir + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatalf("temp file was not cleaned up after rename error")
	}

	// 4. Reader concurrency / truncation test:
	// A file with 50,000 bytes of 'A' is updated to 50,000 bytes of 'B'.
	// A concurrent reader must NEVER observe an empty file (0 bytes) or partial length.
	t.Run("ReaderConcurrencyTruncation", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("skipping concurrent open-reader test: Windows MoveFileEx returns sharing violation when destination is actively open")
		}
		concurrentFile := filepath.Join(tmpDir, "concurrent.txt")
		chunkA := strings.Repeat("A", 50000)
		chunkB := strings.Repeat("B", 50000)
		if err := os.WriteFile(concurrentFile, []byte(chunkA), 0o644); err != nil {
			t.Fatal(err)
		}

		stop := make(chan struct{})
		readerErrCh := make(chan error, 1)

		go func() {
			for {
				select {
				case <-stop:
					readerErrCh <- nil
					return
				default:
					data, err := os.ReadFile(concurrentFile)
					if err != nil {
						readerErrCh <- fmt.Errorf("read error: %w", err)
						return
					}
					if len(data) == 0 {
						readerErrCh <- fmt.Errorf("read observed 0-byte truncated file (O_TRUNC exposure)")
						return
					}
					if len(data) != 50000 {
						readerErrCh <- fmt.Errorf("read observed partial data length: %d", len(data))
						return
					}
					first := data[0]
					for _, b := range data {
						if b != first {
							readerErrCh <- fmt.Errorf("read observed torn read with mixed bytes")
							return
						}
					}
				}
			}
		}()

		// Perform multiple atomic overwrites
		for i := 0; i < 20; i++ {
			var toWrite string
			if i%2 == 0 {
				toWrite = chunkB
			} else {
				toWrite = chunkA
			}
			if err := publisher.AtomicWriteFile(concurrentFile, []byte(toWrite), 0o644); err != nil {
				t.Fatalf("concurrent AtomicWriteFile %d failed: %v", i, err)
			}
		}

		close(stop)
		if err := <-readerErrCh; err != nil {
			t.Fatalf("concurrent reader check failed: %v", err)
		}
	})
}

func TestPublisher_MidBatchWriteFailure_RollbackAndSelfHealing(t *testing.T) {
	bareRemoteDir, repoDir, runGit := setupTestGitRepo(t)

	// Pre-populate with initial legacy file and initial dual-projection files committed
	if err := os.WriteFile(filepath.Join(repoDir, "all.txt"), []byte("legacy-root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "all.txt")
	runGit("commit", "-m", "commit legacy file")
	runGit("push", "origin", "main")
	headInitial := runGit("rev-parse", "HEAD")

	// Add an untracked unrelated user file (notes.txt) and modify README.md (uncommitted)
	// These must remain completely untouched by rollback.
	notesPath := filepath.Join(repoDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("user notes to preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# gemsub - modified readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create store with passing candidates
	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass-1",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})
	st.PutWithTransition(store.Result{
		Link:                   "vmess://pass-2",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
		TestedAt:               time.Now(),
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	// Simulate failure on writing generic/vmess.txt (midway through target files)
	failingPub := publisher.New(cfg, st).WithFSOverrides(
		func(targetPath string, content []byte, perm os.FileMode) error {
			if strings.HasSuffix(targetPath, filepath.Join("generic", "vmess.txt")) {
				return fmt.Errorf("injected disk full error ENOSPC")
			}
			return publisher.AtomicWriteFile(targetPath, content, perm)
		},
		nil,
	)

	// Cycle 1: publish fails midway
	err := failingPub.Publish(context.Background())
	if err == nil {
		t.Fatalf("expected error on mid-batch write failure, got nil")
	}
	if !strings.Contains(err.Error(), "injected disk full error ENOSPC") {
		t.Fatalf("expected ENOSPC error, got: %v", err)
	}

	// Assertions after rollback:
	// 1. No temporary files remain anywhere
	err = filepath.Walk(repoDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if strings.HasSuffix(path, ".tmp") {
			return fmt.Errorf("lingering temp file: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("found temporary files after failed publication: %v", err)
	}

	// 2. Legacy file was restored from HEAD
	legacyBytes, err := os.ReadFile(filepath.Join(repoDir, "all.txt"))
	if err != nil {
		t.Fatalf("expected all.txt to be restored: %v", err)
	}
	if string(legacyBytes) != "legacy-root\n" {
		t.Fatalf("all.txt content mismatch: %q", string(legacyBytes))
	}

	// 3. New files written before the failure (e.g. generic/all.txt, generic/vless.txt) were cleaned up
	if _, err := os.Stat(filepath.Join(repoDir, "generic", "all.txt")); !os.IsNotExist(err) {
		t.Errorf("generic/all.txt should have been removed during rollback")
	}
	if _, err := os.Stat(filepath.Join(repoDir, "generic", "vless.txt")); !os.IsNotExist(err) {
		t.Errorf("generic/vless.txt should have been removed during rollback")
	}

	// 4. Unrelated files are preserved
	notesBytes, err := os.ReadFile(notesPath)
	if err != nil || string(notesBytes) != "user notes to preserve\n" {
		t.Fatalf("unrelated notes.txt was modified or deleted: %v", err)
	}
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil || string(readmeBytes) != "# gemsub - modified readme\n" {
		t.Fatalf("unrelated README.md modification was reverted or deleted: %v", err)
	}

	// 5. Git HEAD is unchanged
	headAfterFail := runGit("rev-parse", "HEAD")
	if headAfterFail != headInitial {
		t.Fatalf("HEAD changed after failed publication: %s != %s", headAfterFail, headInitial)
	}

	// 6. Working tree safety check on publisher-owned files passes cleanly!
	// Cycle 2: Self-healing run with healthy writer succeeds without manual intervention
	healthyPub := publisher.New(cfg, st)
	if err := healthyPub.Publish(context.Background()); err != nil {
		t.Fatalf("cycle 2 (self-healing) publish failed: %v", err)
	}

	// Verify cycle 2 created and pushed new commit
	headCycle2 := runGit("rev-parse", "HEAD")
	if headCycle2 == headInitial {
		t.Fatalf("expected new commit after successful cycle 2")
	}

	// Verify dual projections exist and legacy file was cleanly removed
	if _, err := os.Stat(filepath.Join(repoDir, "generic", "all.txt")); err != nil {
		t.Errorf("generic/all.txt missing after cycle 2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "gemini", "all.txt")); err != nil {
		t.Errorf("gemini/all.txt missing after cycle 2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "all.txt")); !os.IsNotExist(err) {
		t.Errorf("legacy all.txt should have been removed in cycle 2")
	}

	// Verify notes.txt and README.md are still intact
	notesBytes2, _ := os.ReadFile(notesPath)
	if string(notesBytes2) != "user notes to preserve\n" {
		t.Errorf("notes.txt was lost after cycle 2")
	}
}

func TestPublisher_LegacyRemovalFailure_RollbackRestoresDeletedFiles(t *testing.T) {
	bareRemoteDir, repoDir, runGit := setupTestGitRepo(t)

	// Commit multiple legacy root files
	if err := os.WriteFile(filepath.Join(repoDir, "all.txt"), []byte("all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "vless.txt"), []byte("vless\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "all.txt", "vless.txt")
	runGit("commit", "-m", "add legacy files")
	runGit("push", "origin", "main")
	headBefore := runGit("rev-parse", "HEAD")

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass-1",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	// Inject failure on removing vless.txt (after all.txt has already been deleted)
	failingPub := publisher.New(cfg, st).WithFSOverrides(
		nil,
		func(path string) error {
			if strings.HasSuffix(path, "vless.txt") {
				return fmt.Errorf("injected error removing vless.txt")
			}
			return os.Remove(path)
		},
	)

	err := failingPub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "injected error removing vless.txt") {
		t.Fatalf("expected injected error removing vless.txt, got: %v", err)
	}

	// Assert that all.txt was restored by rollback
	if _, err := os.Stat(filepath.Join(repoDir, "all.txt")); err != nil {
		t.Fatalf("expected all.txt to be restored after rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "vless.txt")); err != nil {
		t.Fatalf("expected vless.txt to still exist: %v", err)
	}

	// Git HEAD unchanged
	headAfter := runGit("rev-parse", "HEAD")
	if headAfter != headBefore {
		t.Fatalf("HEAD changed after rollback: %s != %s", headAfter, headBefore)
	}

	// Next cycle with healthy removal succeeds
	healthyPub := publisher.New(cfg, st)
	if err := healthyPub.Publish(context.Background()); err != nil {
		t.Fatalf("healthy cycle publish failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "all.txt")); !os.IsNotExist(err) {
		t.Fatalf("all.txt should be deleted after successful publish")
	}
}

func TestPublisher_GitAddFailure_RollbackRestoresTree(t *testing.T) {
	bareRemoteDir, repoDir, runGit := setupTestGitRepo(t)

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass-1",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	runner := &commandFailingGitRunner{failOnCmd: "add", failOnce: true}
	pub := publisher.NewWithGit(cfg, st, runner)

	// Cycle 1: git add fails
	err := pub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "git add failed") {
		t.Fatalf("expected git add failed error, got: %v", err)
	}

	// Verify working tree is clean for publisher files
	statusOut := runGit("status", "--porcelain", "--", "generic/all.txt", "gemini/all.txt", "meta.json")
	if strings.TrimSpace(statusOut) != "" {
		t.Fatalf("expected clean working tree after git add failure rollback, got: %q", statusOut)
	}

	// Cycle 2: succeeds automatically
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("cycle 2 publish failed: %v", err)
	}
}

func TestPublisher_GitCommitFailure_RollbackRestoresTree(t *testing.T) {
	bareRemoteDir, repoDir, runGit := setupTestGitRepo(t)

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass-1",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	runner := &commandFailingGitRunner{failOnCmd: "commit", failOnce: true}
	pub := publisher.NewWithGit(cfg, st, runner)

	// Cycle 1: git commit fails
	err := pub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "git commit failed") {
		t.Fatalf("expected git commit failed error, got: %v", err)
	}

	// Verify working tree is clean and unstaged
	statusOut := runGit("status", "--porcelain", "--", "generic/all.txt", "gemini/all.txt", "meta.json")
	if strings.TrimSpace(statusOut) != "" {
		t.Fatalf("expected clean working tree after git commit failure rollback, got: %q", statusOut)
	}

	// Cycle 2: succeeds automatically
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("cycle 2 publish failed: %v", err)
	}
}

func TestPublisher_PreExistingManualEdit_AbortsWithoutRollbackOrOverwrite(t *testing.T) {
	bareRemoteDir, repoDir, _ := setupTestGitRepo(t)

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{
		Link:                   "vless://pass-1",
		Status:                 store.StatusPassed,
		TransportEvidenceKnown: true,
		TransportOK:            true,
	})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}
	pub := publisher.New(cfg, st)

	// Initial publication succeeds
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("initial publish failed: %v", err)
	}

	// Operator manually edits generic/all.txt without committing
	targetPath := filepath.Join(repoDir, "generic", "all.txt")
	manualContent := "manual edit that must be protected\n"
	if err := os.WriteFile(targetPath, []byte(manualContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// Next publication must abort due to safety check
	err := pub.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
		t.Fatalf("expected pre-existing uncommitted changes error, got: %v", err)
	}

	// The manual content must NOT have been overwritten or rolled back
	currentContent, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(currentContent) != manualContent {
		t.Fatalf("manual edit was lost! expected %q, got %q", manualContent, string(currentContent))
	}
}

func TestPublisher_RollbackFailure_SurfacesBothErrors(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://1", Status: store.StatusPassed, TransportEvidenceKnown: true, TransportOK: true})
	st.FinishCycle()

	mock := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				cmd := args[0]
				if cmd == "rev-parse" {
					return "true\n", nil
				}
				if cmd == "config" {
					return "git@github.com:example/repo.git\n", nil
				}
				if cmd == "status" {
					return "", nil
				}
				if cmd == "add" {
					return "", fmt.Errorf("primary add failure")
				}
				if cmd == "reset" || cmd == "checkout" {
					return "", fmt.Errorf("secondary rollback git failure")
				}
			}
			return "", nil
		},
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "git@github.com:example/repo.git",
	}
	pub := publisher.NewWithGit(cfg, st, mock)

	err := pub.Publish(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}

	// Both primary error and rollback error must be included
	if !strings.Contains(err.Error(), "primary add failure") {
		t.Errorf("expected primary add failure in error: %v", err)
	}
	if !strings.Contains(err.Error(), "rollback failed") {
		t.Errorf("expected rollback failure in error: %v", err)
	}
	if !strings.Contains(err.Error(), "secondary rollback git failure") {
		t.Errorf("expected secondary rollback git failure in error: %v", err)
	}
}

func TestPublisher_Publish_CapturesExactCommitBeforePush_ImmuneToLaterHEADChanges(t *testing.T) {
	bareRemoteDir := t.TempDir()
	initBare := exec.Command("git", "init", "--bare", "-b", "main")
	initBare.Dir = bareRemoteDir
	if out, err := initBare.CombinedOutput(); err != nil {
		t.Fatalf("git init bare failed: %v: %s", err, string(out))
	}

	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, string(out))
		}
		return strings.TrimSpace(string(out))
	}

	runGit("init", "-b", "main")
	runGit("config", "user.name", "Gemsub Test")
	runGit("config", "user.email", "test@example.com")
	runGit("remote", "add", "origin", bareRemoteDir)

	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# gemsub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-m", "initial commit")
	runGit("push", "origin", "main")

	st := store.New(filepath.Join(t.TempDir(), "state.json"), 2)
	st.PutWithTransition(store.Result{Link: "vless://user@1.1.1.1:443", Status: store.StatusPassed})
	st.FinishCycle()

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: repoDir,
		Branch:     "main",
		RemoteURL:  bareRemoteDir,
	}

	pub := publisher.New(cfg, st)

	// Before publication, LastPublishedCommit is empty
	if lpc := pub.LastPublishedCommit(); lpc != "" {
		t.Fatalf("expected empty initial LastPublishedCommit, got %q", lpc)
	}

	// 1. First publication creates a commit and pushes it
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	pubSHA := pub.LastPublishedCommit()
	if pubSHA == "" {
		t.Fatal("expected non-empty LastPublishedCommit after successful publish")
	}

	// Disk HEAD immediately matches published commit
	currentHead := pub.LastCommit(context.Background())
	if currentHead != pubSHA {
		t.Fatalf("expected LastCommit %q == LastPublishedCommit %q", currentHead, pubSHA)
	}

	// 2. Simulate an external commit made and pushed to the repository
	runGit("commit", "--allow-empty", "-m", "unrelated external commit")
	runGit("push", "origin", "main")

	newDiskHead := pub.LastCommit(context.Background())
	if newDiskHead == pubSHA {
		t.Fatalf("expected LastCommit to change after external commit, but got %q", newDiskHead)
	}

	// LastPublishedCommit must remain strictly immutable to subsequent external HEAD changes!
	if lpc := pub.LastPublishedCommit(); lpc != pubSHA {
		t.Fatalf("LastPublishedCommit mutated by external commit! expected %q, got %q", pubSHA, lpc)
	}

	// 3. Subsequent publish with identical content (no-op) must preserve pubSHA
	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("no-op Publish failed: %v", err)
	}
	if lpc := pub.LastPublishedCommit(); lpc != pubSHA {
		t.Fatalf("no-op Publish mutated LastPublishedCommit! expected %q, got %q", pubSHA, lpc)
	}

	// 4. Publication with new content produces and records a new exact published commit
	st.PutWithTransition(store.Result{Link: "vmess://user@2.2.2.2:443", Status: store.StatusPassed})
	st.FinishCycle()

	if err := pub.Publish(context.Background()); err != nil {
		t.Fatalf("second Publish failed: %v", err)
	}
	secondPubSHA := pub.LastPublishedCommit()
	if secondPubSHA == "" || secondPubSHA == pubSHA {
		t.Fatalf("expected new distinct LastPublishedCommit after second publish; got %q", secondPubSHA)
	}
	if pub.LastCommit(context.Background()) != secondPubSHA {
		t.Fatalf("expected disk HEAD to match new published commit %q", secondPubSHA)
	}
}
