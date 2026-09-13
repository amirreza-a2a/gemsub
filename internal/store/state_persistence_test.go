package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"gemsub/internal/store"
)

func TestStoreSave_CreatesParentDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	nestedPath := filepath.Join(tmpDir, "nested", "deep", "dir", "state.json")

	st := store.New(nestedPath, 2)
	st.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#Test",
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("st.Save() failed on non-existent parent directory: %v", err)
	}

	primaryPath := st.PrimaryPath()
	if _, err := os.Stat(primaryPath); err != nil {
		t.Fatalf("expected primary state file %s to exist: %v", primaryPath, err)
	}

	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(filepath.Dir(primaryPath))
		if err != nil {
			t.Fatalf("stat parent dir: %v", err)
		}
		if got := dirInfo.Mode().Perm(); got != 0o700 {
			t.Errorf("expected parent directory mode 0700, got %04o", got)
		}
	}
}

func TestStoreSave_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not applicable on Windows")
	}

	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state_dir", "state.json")

	st := store.New(statePath, 2)
	st.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#Test",
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("st.Save() failed: %v", err)
	}

	primaryPath := st.PrimaryPath()
	fi, err := os.Stat(primaryPath)
	if err != nil {
		t.Fatalf("stat primary file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("expected newly created state file mode 0600, got %04o", got)
	}
}

func TestStoreSave_PreservesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not applicable on Windows")
	}

	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	primaryPath := statePath + ".gz"

	// Pre-create the primary file with 0640
	if err := os.WriteFile(primaryPath, []byte("placeholder"), 0o640); err != nil {
		t.Fatalf("pre-create state file: %v", err)
	}
	if err := os.Chmod(primaryPath, 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	st := store.New(statePath, 2)
	st.PutWithTransition(store.Result{
		Link:     "vless://test@1.1.1.1:443#Test",
		Status:   store.StatusPassed,
		Latency:  100 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("st.Save() failed: %v", err)
	}

	fi, err := os.Stat(primaryPath)
	if err != nil {
		t.Fatalf("stat primary file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("expected preserved mode 0640, got %04o", got)
	}
}

func TestStoreLoad_NoSilentCWDFallback(t *testing.T) {
	// Create an isolated temp directory to act as simulated CWD
	cwdDir := t.TempDir()
	cwdState := filepath.Join(cwdDir, "gemsub_state.json.gz")

	// Pre-populate old CWD state file with a dummy snapshot
	stOld := store.New(filepath.Join(cwdDir, "gemsub_state.json"), 2)
	stOld.PutWithTransition(store.Result{
		Link:     "vless://old-cwd@1.1.1.1:443#OldCWD",
		Status:   store.StatusPassed,
		Latency:  50 * time.Millisecond,
		TestedAt: time.Now(),
	})
	stOld.FinishCycle()
	if err := stOld.Save(); err != nil {
		t.Fatalf("save old cwd state: %v", err)
	}
	if _, err := os.Stat(cwdState); err != nil {
		t.Fatalf("expected old cwd state file to exist: %v", err)
	}

	// Change process directory temporarily to simulated CWD
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	// Now instantiate a store pointing to a different canonical path where no state exists
	canonicalDir := t.TempDir()
	canonicalPath := filepath.Join(canonicalDir, "state.json")
	st := store.New(canonicalPath, 2)

	if err := st.Load(); err != nil {
		t.Fatalf("st.Load() error: %v", err)
	}

	// Verify that the old candidate from CWD was NOT loaded
	if got := len(st.Passing()); got != 0 {
		t.Fatalf("expected 0 passing candidates on cold start, got %d (silent CWD load occurred)", got)
	}
	if got := len(st.NetworkPassing()); got != 0 {
		t.Fatalf("expected 0 network passing candidates on cold start, got %d", got)
	}
}

func TestStore_CompressedAndLegacyUncompressedLoading(t *testing.T) {
	// Case 1: Compressed .gz persistence round-trip
	tmpDir1 := t.TempDir()
	path1 := filepath.Join(tmpDir1, "state.json")
	st1 := store.New(path1, 2)
	link1 := "vless://gz-test@1.1.1.1:443#GzTest"
	st1.PutWithTransition(store.Result{
		Link:     link1,
		Status:   store.StatusPassed,
		Latency:  40 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st1.FinishCycle()
	if err := st1.Save(); err != nil {
		t.Fatalf("st1.Save: %v", err)
	}

	st1Reload := store.New(path1, 2)
	if err := st1Reload.Load(); err != nil {
		t.Fatalf("st1Reload.Load: %v", err)
	}
	passing1 := st1Reload.Passing()
	if len(passing1) != 1 || passing1[0] != link1 {
		t.Fatalf("expected 1 passing candidate %q, got %+v", link1, passing1)
	}

	// Case 2: Uncompressed raw JSON legacy fallback (no .gz file present)
	tmpDir2 := t.TempDir()
	path2 := filepath.Join(tmpDir2, "state.json")
	link2 := "vless://raw-test@2.2.2.2:443#RawTest"
	rawSnap := store.Snapshot{
		Version:    1,
		LastCycle:  time.Now(),
		CycleCount: 1,
		Results: []store.Result{
			{
				Link:     link2,
				Status:   store.StatusPassed,
				Passed:   true,
				Latency:  60 * time.Millisecond,
				TestedAt: time.Now(),
			},
		},
	}
	rawBytes, err := json.Marshal(rawSnap)
	if err != nil {
		t.Fatalf("marshal raw: %v", err)
	}
	if err := os.WriteFile(path2, rawBytes, 0o600); err != nil {
		t.Fatalf("write raw file: %v", err)
	}

	st2 := store.New(path2, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("st2.Load legacy raw: %v", err)
	}
	passing2 := st2.Passing()
	if len(passing2) != 1 || passing2[0] != link2 {
		t.Fatalf("expected 1 passing candidate %q from legacy fallback, got %+v", link2, passing2)
	}

	// Case 3: Both .gz and raw exist: .gz takes precedence
	st2Updated := store.New(path2, 2)
	link2Updated := "vless://gz-precedence@3.3.3.3:443#GzPrecedence"
	st2Updated.PutWithTransition(store.Result{
		Link:     link2Updated,
		Status:   store.StatusPassed,
		Latency:  30 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st2Updated.FinishCycle()
	if err := st2Updated.Save(); err != nil {
		t.Fatalf("st2Updated.Save: %v", err)
	}

	st2Reload := store.New(path2, 2)
	if err := st2Reload.Load(); err != nil {
		t.Fatalf("st2Reload.Load: %v", err)
	}
	passing3 := st2Reload.Passing()
	if len(passing3) != 1 || passing3[0] != link2Updated {
		t.Fatalf("expected .gz precedence with %q, got %+v", link2Updated, passing3)
	}
}

func TestStoreSave_StatErrorPropagation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission-based stat errors are not applicable on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}

	tmpDir := t.TempDir()
	restrictedDir := filepath.Join(tmpDir, "restricted")
	if err := os.MkdirAll(restrictedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	statePath := filepath.Join(restrictedDir, "state.json")
	primaryPath := statePath + ".gz"
	if err := os.WriteFile(primaryPath, []byte("dummy"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// Make the parent directory unsearchable (0o000) so os.Stat(primary) fails with permission denied
	if err := os.Chmod(restrictedDir, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(restrictedDir, 0o755)
	})

	st := store.New(statePath, 2)
	err := st.Save()
	if err == nil {
		t.Fatal("expected Store.Save() to fail when stat/directory returns permission denied")
	}
	if !strings.Contains(err.Error(), "stat existing state file") && !strings.Contains(err.Error(), "ensure state directory") {
		t.Errorf("expected error to mention stat/directory failure, got: %v", err)
	}
}

func TestHermeticStore_DeepNestedParentDirectoryCreation(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDirs := []string{"level1", "level2", "level3", "level4", "level5"}
	stateDir := filepath.Join(append([]string{tmpDir}, nestedDirs...)...)
	statePath := filepath.Join(stateDir, "state.json")

	// Verify with os.Stat that the target directory and intermediate directories do not exist before Save()
	for i := 1; i <= len(nestedDirs); i++ {
		checkPath := filepath.Join(append([]string{tmpDir}, nestedDirs[:i]...)...)
		if _, err := os.Stat(checkPath); !os.IsNotExist(err) {
			t.Fatalf("expected directory %s not to exist prior to Save()", checkPath)
		}
	}

	st := store.New(statePath, 2)
	link1 := "vless://node1@1.1.1.1:443#Node1"
	st.PutWithTransition(store.Result{
		Link:     link1,
		Status:   store.StatusPassed,
		Latency:  45 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st.FinishCycle()

	if err := st.Save(); err != nil {
		t.Fatalf("st.Save() failed on deep nested path: %v", err)
	}

	primaryPath := st.PrimaryPath()
	fi, err := os.Stat(primaryPath)
	if err != nil {
		t.Fatalf("expected primary file %s to exist: %v", primaryPath, err)
	}
	if fi.IsDir() {
		t.Fatalf("expected primary path %s to be a file, not a directory", primaryPath)
	}
	if fi.Size() == 0 {
		t.Fatalf("expected primary file %s to have non-zero size", primaryPath)
	}

	if runtime.GOOS != "windows" {
		if got := fi.Mode().Perm(); got != 0o600 {
			t.Errorf("expected primary file mode 0600, got %04o", got)
		}

		// Verify every created directory in the hierarchy has 0o700 permission
		for i := 1; i <= len(nestedDirs); i++ {
			dirPath := filepath.Join(append([]string{tmpDir}, nestedDirs[:i]...)...)
			dirInfo, err := os.Stat(dirPath)
			if err != nil {
				t.Fatalf("os.Stat(%s) failed: %v", dirPath, err)
			}
			if !dirInfo.IsDir() {
				t.Errorf("expected %s to be a directory", dirPath)
			}
			if got := dirInfo.Mode().Perm(); got != 0o700 {
				t.Errorf("expected directory %s mode 0700, got %04o", dirPath, got)
			}
		}
	}

	// Verify persistence round-trip by reloading into a separate Store instance
	st2 := store.New(statePath, 2)
	if err := st2.Load(); err != nil {
		t.Fatalf("st2.Load() failed: %v", err)
	}
	passing := st2.Passing()
	if len(passing) != 1 || passing[0] != link1 {
		t.Fatalf("expected passing %v, got %v", []string{link1}, passing)
	}

	// Second cycle save to verify idempotency and existing directory handling
	link2 := "vless://node2@2.2.2.2:443#Node2"
	st2.PutWithTransition(store.Result{
		Link:     link2,
		Status:   store.StatusPassed,
		Latency:  35 * time.Millisecond,
		TestedAt: time.Now(),
	})
	st2.FinishCycle()
	if err := st2.Save(); err != nil {
		t.Fatalf("st2.Save() second cycle failed: %v", err)
	}

	st3 := store.New(statePath, 2)
	if err := st3.Load(); err != nil {
		t.Fatalf("st3.Load() failed: %v", err)
	}
	if len(st3.Passing()) != 2 {
		t.Fatalf("expected 2 passing candidates, got %d", len(st3.Passing()))
	}
}

func TestHermeticStore_PermissionHardening_ModeMatrix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not applicable on Windows")
	}

	testModes := []os.FileMode{
		0o600,
		0o640,
		0o644,
		0o660,
	}

	for _, initialMode := range testModes {
		t.Run(initialMode.String(), func(t *testing.T) {
			tmpDir := t.TempDir()
			statePath := filepath.Join(tmpDir, "state.json")
			primaryPath := statePath + ".gz"

			// Create pre-existing state file with specific permissions
			if err := os.WriteFile(primaryPath, []byte("pre-existing placeholder data"), initialMode); err != nil {
				t.Fatalf("failed to write initial state file: %v", err)
			}
			if err := os.Chmod(primaryPath, initialMode); err != nil {
				t.Fatalf("failed to chmod initial state file: %v", err)
			}

			// Verify with os.Stat that pre-existing mode is established
			fiBefore, err := os.Stat(primaryPath)
			if err != nil {
				t.Fatalf("stat before Save: %v", err)
			}
			if got := fiBefore.Mode().Perm(); got != initialMode {
				t.Fatalf("initial mode setup failed: got %04o, want %04o", got, initialMode)
			}

			// Instantiate store and save a valid snapshot
			st := store.New(statePath, 2)
			st.PutWithTransition(store.Result{
				Link:     "vless://perm-test@1.1.1.1:443#PermTest",
				Status:   store.StatusPassed,
				Latency:  50 * time.Millisecond,
				TestedAt: time.Now(),
			})
			st.FinishCycle()

			if err := st.Save(); err != nil {
				t.Fatalf("st.Save() failed: %v", err)
			}

			// Assert via os.Stat that file permissions were preserved
			fiAfter, err := os.Stat(primaryPath)
			if err != nil {
				t.Fatalf("stat after Save: %v", err)
			}
			if got := fiAfter.Mode().Perm(); got != initialMode {
				t.Errorf("expected preserved mode %04o, got %04o", initialMode, got)
			}
			if fiAfter.Size() == 0 {
				t.Error("expected non-empty state file after Save()")
			}
		})
	}
}
