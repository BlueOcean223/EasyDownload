//go:build darwin

package proxy

import (
	"os"
	"testing"
)

func TestBuildDarwinCertTrustCommandUsesVerifyCert(t *testing.T) {
	command := buildDarwinCertTrustCommand("/tmp/ca.crt")

	if len(command) == 0 || command[0] != "/usr/bin/security" {
		t.Fatalf("unexpected command: %#v", command)
	}
	if !containsArg(command, "verify-cert") {
		t.Fatalf("expected verify-cert command, got %#v", command)
	}
	if containsArg(command, "find-certificate") {
		t.Fatalf("expected no find-certificate trust check, got %#v", command)
	}
	if !containsArg(command, "-l") {
		t.Fatalf("expected CA leaf verification flag, got %#v", command)
	}
}

func TestIsCertInstalledFalseForUntrustedLocalCertificate(t *testing.T) {
	certDir := t.TempDir()
	cm := NewCertManager(certDir)
	if err := cm.GenerateCACert(); err != nil {
		t.Fatalf("GenerateCACert returned error: %v", err)
	}

	if cm.IsCertInstalled() {
		t.Fatal("expected untrusted local certificate to be reported as not installed")
	}

	if _, err := os.Stat(cm.CertPath); err != nil {
		t.Fatalf("expected generated cert to remain on disk: %v", err)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
