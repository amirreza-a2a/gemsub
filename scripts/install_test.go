package scripts_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" {
		// Linux/Termux bash installer scripts are not executed on Windows.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// createMockReleaseArchive creates a .tar.gz archive containing a mock 'gemsub' binary
// and returns the archive bytes and its SHA-256 hex string.
func createMockReleaseArchive(t *testing.T, content string) ([]byte, string) {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	binaryContent := []byte(content)
	hdr := &tar.Header{
		Name: "gemsub",
		Mode: 0755,
		Size: int64(len(binaryContent)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("failed to write tar header: %v", err)
	}
	if _, err := tw.Write(binaryContent); err != nil {
		t.Fatalf("failed to write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("failed to close tar writer: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip writer: %v", err)
	}

	archiveBytes := buf.Bytes()
	hash := sha256.Sum256(archiveBytes)
	return archiveBytes, hex.EncodeToString(hash[:])
}

func getProjectRoot(t *testing.T) string {
	t.Helper()
	// Since this test runs in scripts/, project root is parent directory
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}
	if filepath.Base(wd) == "scripts" {
		return filepath.Dir(wd)
	}
	return wd
}

func TestLinuxInstaller_SupportedArchitectures(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	archCases := []struct {
		unameArch   string
		archiveName string
	}{
		{"x86_64", "gemsub_Linux_x86_64.tar.gz"},
		{"amd64", "gemsub_Linux_x86_64.tar.gz"},
		{"aarch64", "gemsub_Linux_arm64.tar.gz"},
		{"arm64", "gemsub_Linux_arm64.tar.gz"},
		{"armv7l", "gemsub_Linux_armv7.tar.gz"},
		{"armv7", "gemsub_Linux_armv7.tar.gz"},
	}

	for _, tc := range archCases {
		t.Run("Arch_"+tc.unameArch, func(t *testing.T) {
			fixtureDir := t.TempDir()
			installDir := t.TempDir()

			archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-test'\n")
			archiveFile := filepath.Join(fixtureDir, tc.archiveName)
			if err := os.WriteFile(archiveFile, archiveBytes, 0644); err != nil {
				t.Fatalf("failed to write archive fixture: %v", err)
			}

			checksumContent := fmt.Sprintf("%s  %s\n", hash, tc.archiveName)
			if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
				t.Fatalf("failed to write checksums fixture: %v", err)
			}

			cmd := exec.Command("bash", scriptPath)
			cmd.Env = append(os.Environ(),
				"ARCH_OVERRIDE="+tc.unameArch,
				"OS_OVERRIDE=Linux",
				"BASE_URL=file://"+fixtureDir,
				"INSTALL_DIR="+installDir,
				"TERMUX_VERSION=", // ensure not treated as Termux
			)

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installer failed for arch %s: %v\nOutput:\n%s", tc.unameArch, err, string(out))
			}

			installedBinary := filepath.Join(installDir, "gemsub")
			info, err := os.Stat(installedBinary)
			if err != nil {
				t.Fatalf("expected binary at %s, got error: %v", installedBinary, err)
			}
			if info.Mode()&0111 == 0 {
				t.Errorf("expected binary to be executable, mode is %v", info.Mode())
			}
		})
	}
}

func TestLinuxInstaller_UnsupportedArchitecture(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=mips64",
		"OS_OVERRIDE=Linux",
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on unsupported arch mips64, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Unsupported architecture") {
		t.Errorf("expected error message to mention 'Unsupported architecture', got:\n%s", string(out))
	}
}

func TestLinuxInstaller_UnsupportedOS(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"OS_OVERRIDE=Darwin",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on unsupported OS Darwin, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Unsupported operating system") {
		t.Errorf("expected error message to mention 'Unsupported operating system', got:\n%s", string(out))
	}
}

func TestLinuxInstaller_ChecksumMismatchFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	fixtureDir := t.TempDir()
	installDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-test'\n")
	archiveName := "gemsub_Linux_x86_64.tar.gz"
	archiveFile := filepath.Join(fixtureDir, archiveName)
	if err := os.WriteFile(archiveFile, archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Deliberately corrupted expected hash
	tamperedHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	checksumContent := fmt.Sprintf("%s  %s\n", tamperedHash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=x86_64",
		"OS_OVERRIDE=Linux",
		"BASE_URL=file://"+fixtureDir,
		"INSTALL_DIR="+installDir,
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail on checksum mismatch, but succeeded:\n%s", string(out))
	}

	if !strings.Contains(string(out), "Checksum verification failed") {
		t.Errorf("expected output to mention 'Checksum verification failed', got:\n%s", string(out))
	}

	// Invariant: No binary should be installed when checksum verification fails
	installedBinary := filepath.Join(installDir, "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on checksum failure", installedBinary)
	}
}

func TestTermuxInstaller_Success(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	checksumContent := fmt.Sprintf("%s  %s\n", hash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Termux installer failed: %v\nOutput:\n%s", err, string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	info, err := os.Stat(installedBinary)
	if err != nil {
		t.Fatalf("expected binary at %s, got error: %v", installedBinary, err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("expected binary to be executable, mode is %v", info.Mode())
	}
}

func TestTermuxInstaller_NonTermuxRejection(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=0",
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on non-Termux system, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "intended exclusively for Termux") {
		t.Errorf("expected output to mention 'intended exclusively for Termux', got:\n%s", string(out))
	}
}

func TestTermuxInstaller_UnsupportedArchitecture(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=x86_64",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on Termux x86_64, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Unsupported Termux architecture") {
		t.Errorf("expected output to mention 'Unsupported Termux architecture', got:\n%s", string(out))
	}
}

func TestTermuxInstaller_ChecksumMismatchFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	tamperedHash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	checksumContent := fmt.Sprintf("%s  %s\n", tamperedHash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on checksum mismatch, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Checksum verification failed") {
		t.Errorf("expected output to mention 'Checksum verification failed', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on checksum failure", installedBinary)
	}
}

func TestLinuxInstaller_ChecksumMalformedFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	fixtureDir := t.TempDir()
	installDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-test'\n")
	archiveName := "gemsub_Linux_x86_64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Malformed hash (not 64 hex characters)
	malformedHash := "not-a-valid-sha256-hash"
	checksumContent := fmt.Sprintf("%s  %s\n", malformedHash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=x86_64",
		"OS_OVERRIDE=Linux",
		"BASE_URL=file://"+fixtureDir,
		"INSTALL_DIR="+installDir,
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail on malformed checksum, but succeeded:\n%s", string(out))
	}

	if !strings.Contains(string(out), "Malformed SHA-256 checksum") {
		t.Errorf("expected output to mention 'Malformed SHA-256 checksum', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(installDir, "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on malformed checksum failure", installedBinary)
	}
}

func TestLinuxInstaller_ChecksumMissingFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	fixtureDir := t.TempDir()
	installDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-test'\n")
	archiveName := "gemsub_Linux_x86_64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Checksums file missing the target archive
	checksumContent := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  unrelated_archive.tar.gz\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=x86_64",
		"OS_OVERRIDE=Linux",
		"BASE_URL=file://"+fixtureDir,
		"INSTALL_DIR="+installDir,
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail on missing checksum entry, but succeeded:\n%s", string(out))
	}

	if !strings.Contains(string(out), "not found in release checksums") {
		t.Errorf("expected output to mention 'not found in release checksums', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(installDir, "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on missing checksum failure", installedBinary)
	}
}

func TestLinuxInstaller_ChecksumDuplicateFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	fixtureDir := t.TempDir()
	installDir := t.TempDir()

	archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-test'\n")
	archiveName := "gemsub_Linux_x86_64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Duplicate entries in checksums.txt
	checksumContent := fmt.Sprintf("%s  %s\n%s  %s\n", hash, archiveName, hash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=x86_64",
		"OS_OVERRIDE=Linux",
		"BASE_URL=file://"+fixtureDir,
		"INSTALL_DIR="+installDir,
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected installer to fail on duplicate checksum entries, but succeeded:\n%s", string(out))
	}

	if !strings.Contains(string(out), "multiple checksum entries") {
		t.Errorf("expected output to mention 'multiple checksum entries', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(installDir, "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on duplicate checksum failure", installedBinary)
	}
}

func TestTermuxInstaller_ChecksumMalformedFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	checksumContent := fmt.Sprintf("invalid-hash-short  %s\n", archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on malformed hash, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "Malformed SHA-256 checksum") {
		t.Errorf("expected output to mention 'Malformed SHA-256 checksum', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on malformed checksum failure", installedBinary)
	}
}

func TestTermuxInstaller_ChecksumMissingFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, _ := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	checksumContent := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855  other.tar.gz\n"
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on missing hash, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "not found in release checksums") {
		t.Errorf("expected output to mention 'not found in release checksums', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on missing checksum failure", installedBinary)
	}
}

func TestTermuxInstaller_ChecksumDuplicateFailure(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	checksumContent := fmt.Sprintf("%s  %s\n%s  %s\n", hash, archiveName, hash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected failure on duplicate checksum entries, but succeeded:\n%s", string(out))
	}
	if !strings.Contains(string(out), "multiple checksum entries") {
		t.Errorf("expected output to mention 'multiple checksum entries', got:\n%s", string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	if _, err := os.Stat(installedBinary); !os.IsNotExist(err) {
		t.Errorf("expected no binary installed at %s on duplicate checksum failure", installedBinary)
	}
}

func TestLinuxInstaller_ChecksumBinaryModeMarkerAccepted(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	fixtureDir := t.TempDir()
	installDir := t.TempDir()

	archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-linux-bin'\n")
	archiveName := "gemsub_Linux_x86_64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Line formatted with binary-mode indicator "*": "<hash> *<filename>"
	checksumContent := fmt.Sprintf("%s *%s\n", hash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"ARCH_OVERRIDE=x86_64",
		"OS_OVERRIDE=Linux",
		"BASE_URL=file://"+fixtureDir,
		"INSTALL_DIR="+installDir,
		"TERMUX_VERSION=",
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected binary-mode checksum marker to be accepted, failed: %v\nOutput:\n%s", err, string(out))
	}

	installedBinary := filepath.Join(installDir, "gemsub")
	info, err := os.Stat(installedBinary)
	if err != nil {
		t.Fatalf("expected installed binary at %s: %v", installedBinary, err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("expected installed binary to be executable, mode is %v", info.Mode())
	}
}

func TestTermuxInstaller_ChecksumBinaryModeMarkerAccepted(t *testing.T) {
	root := getProjectRoot(t)
	scriptPath := filepath.Join(root, "scripts", "install-termux.sh")

	fixtureDir := t.TempDir()
	prefixDir := t.TempDir()

	archiveBytes, hash := createMockReleaseArchive(t, "#!/bin/sh\necho 'gemsub-termux-bin'\n")
	archiveName := "gemsub_Termux_arm64.tar.gz"
	if err := os.WriteFile(filepath.Join(fixtureDir, archiveName), archiveBytes, 0644); err != nil {
		t.Fatalf("failed to write archive fixture: %v", err)
	}

	// Line formatted with binary-mode indicator "*": "<hash> *<filename>"
	checksumContent := fmt.Sprintf("%s *%s\n", hash, archiveName)
	if err := os.WriteFile(filepath.Join(fixtureDir, "checksums.txt"), []byte(checksumContent), 0644); err != nil {
		t.Fatalf("failed to write checksums fixture: %v", err)
	}

	cmd := exec.Command("bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"TERMUX_OVERRIDE=1",
		"ARCH_OVERRIDE=aarch64",
		"PREFIX_OVERRIDE="+prefixDir,
		"BASE_URL=file://"+fixtureDir,
	)

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected binary-mode checksum marker to be accepted in Termux installer, failed: %v\nOutput:\n%s", err, string(out))
	}

	installedBinary := filepath.Join(prefixDir, "bin", "gemsub")
	info, err := os.Stat(installedBinary)
	if err != nil {
		t.Fatalf("expected installed binary at %s: %v", installedBinary, err)
	}
	if info.Mode()&0111 == 0 {
		t.Errorf("expected installed binary to be executable, mode is %v", info.Mode())
	}
}
