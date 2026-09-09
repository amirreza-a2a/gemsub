package publisher_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
