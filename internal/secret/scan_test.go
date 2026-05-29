package secret

import "testing"

func TestScanTextFindsSecretAssignments(t *testing.T) {
	findings := ScanText("smtp_user: alice@example.com\nsmtp_pass: supersecret123\n")
	if len(findings) != 2 {
		t.Fatalf("findings = %#v", findings)
	}
	if findings[0].Line != 1 || findings[0].Key != "smtp_user" || findings[1].Line != 2 || findings[1].Key != "smtp_pass" {
		t.Fatalf("unexpected finding %#v", findings[0])
	}
}

func TestScanTextAllowsSecretReferences(t *testing.T) {
	text := "required_secrets:\n  - id: mail.smtp_pass\n    env: SMTP_PASS\npassword: $SMTP_PASS\n"
	if findings := ScanText(text); len(findings) != 0 {
		t.Fatalf("expected no findings for references, got %#v", findings)
	}
}
