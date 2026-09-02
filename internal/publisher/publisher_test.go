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

	// Expected sorted order and duplicate removal
	allContent := string(files1[publisher.FileAll])
	expectedAll := "trojan://ccc\nvless://aaa\nvless://bbb\nvmess://abc\nvmess://xyz\n"
	if allContent != expectedAll {
		t.Fatalf("all.txt mismatch:\nexpected:\n%s\ngot:\n%s", expectedAll, allContent)
	}

	// Verify exact count of duplicates in all.txt
	if strings.Count(allContent, "vless://aaa") != 1 {
		t.Errorf("expected vless://aaa to appear exactly once, got count %d", strings.Count(allContent, "vless://aaa"))
	}
	if strings.Count(allContent, "vmess://xyz") != 1 {
		t.Errorf("expected vmess://xyz to appear exactly once, got count %d", strings.Count(allContent, "vmess://xyz"))
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

	all := string(files[publisher.FileAll])
	if !strings.Contains(all, "vless://passed") {
		t.Errorf("expected vless://passed to be in all.txt")
	}
	if !strings.Contains(all, "vmess://inconclusive-lkg") {
		t.Errorf("expected vmess://inconclusive-lkg to be in all.txt (last-known-good)")
	}
	if strings.Contains(all, "vless://failed") {
		t.Errorf("vless://failed must NOT be in all.txt")
	}
	if strings.Contains(all, "trojan://inconclusive-fresh") {
		t.Errorf("trojan://inconclusive-fresh must NOT be in all.txt")
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

	vless := string(files[publisher.FileVLESS])
	expectedVless := "vless://vless-1\nvless://vless-2\n"
	if vless != expectedVless {
		t.Errorf("vless.txt mismatch: expected %q, got %q", expectedVless, vless)
	}

	vmess := string(files[publisher.FileVMess])
	expectedVmess := "vmess://vmess-1\n"
	if vmess != expectedVmess {
		t.Errorf("vmess.txt mismatch: expected %q, got %q", expectedVmess, vmess)
	}

	trojan := string(files[publisher.FileTrojan])
	expectedTrojan := "trojan://trojan-1\n"
	if trojan != expectedTrojan {
		t.Errorf("trojan.txt mismatch: expected %q, got %q", expectedTrojan, trojan)
	}

	all := string(files[publisher.FileAll])
	if !strings.Contains(all, "ss://shadowsocks-1") {
		t.Errorf("all.txt should include other servable protocols (ss://)")
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
	if meta.VLESSCount != 2 {
		t.Errorf("expected VLESSCount=2, got %d", meta.VLESSCount)
	}
	if meta.VMessCount != 1 {
		t.Errorf("expected VMessCount=1, got %d", meta.VMessCount)
	}
	if meta.TrojanCount != 1 {
		t.Errorf("expected TrojanCount=1, got %d", meta.TrojanCount)
	}
	if _, err := time.Parse(time.RFC3339, meta.GeneratedAt); err != nil {
		t.Errorf("expected RFC3339 generated_at, got %q", meta.GeneratedAt)
	}
}

func TestPublisher_WorkingTreeSafety(t *testing.T) {
	tmpDir := t.TempDir()
	st := store.New(filepath.Join(tmpDir, "state.json"), 2)

	// Pre-existing unstaged changes to all.txt
	mockUnstaged := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:amirreza-a2a/gemsub-subscriptions.git\n", nil
				case "status":
					return " M all.txt\n", nil
				}
			}
			return "", nil
		},
	}

	cfg := &config.PublishingConfig{
		Enabled:    true,
		Repository: tmpDir,
		RemoteURL:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
	}
	pubUnstaged := publisher.NewWithGit(cfg, st, mockUnstaged)
	err := pubUnstaged.Publish(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pre-existing uncommitted changes") {
		t.Fatalf("expected error on pre-existing unstaged changes, got: %v", err)
	}

	// Pre-existing staged changes to vless.txt
	mockStaged := &mockGitRunner{
		runFunc: func(ctx context.Context, dir string, args ...string) (string, error) {
			if len(args) > 0 {
				switch args[0] {
				case "rev-parse":
					return "true\n", nil
				case "config":
					return "git@github.com:amirreza-a2a/gemsub-subscriptions.git\n", nil
				case "status":
					return "M  vless.txt\n", nil
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
		RemoteURL:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
	}
	pubWrongRemote := publisher.NewWithGit(cfgWrongRemote, st, mockWrongRemote)
	if err := pubWrongRemote.Publish(context.Background()); err == nil {
		t.Fatalf("expected error for remote URL mismatch")
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
							return "git@github.com:amirreza-a2a/gemsub-subscriptions.git\n", nil
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
				RemoteURL:  "git@github.com:amirreza-a2a/gemsub-subscriptions.git",
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
