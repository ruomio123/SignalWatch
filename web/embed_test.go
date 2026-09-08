package web

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedFrontendIncludesSubscriptionWorkspaceContract(t *testing.T) {
	script, err := fs.ReadFile(appFiles, "assets/app.js")
	if err != nil {
		t.Fatalf("read embedded app script: %v", err)
	}

	contents := string(script)
	required := []string{
		`href="/subscriptions"`,
		`apiRequest("/sources"`,
		`apiRequest("/subscriptions"`,
		"apiRequest(`/subscriptions/${",
		`headers: { "If-Match":`,
		`const rules = {
    categories,
    include_keywords: includeKeywords,
  }`,
		`type="radio" name="categories"`,
		`请选择 1 个分类`,
		`SUBSCRIPTION_VERSION_CONFLICT`,
	}
	for _, fragment := range required {
		if !strings.Contains(contents, fragment) {
			t.Errorf("embedded subscription workspace is missing %q", fragment)
		}
	}
	for _, forbiddenPayload := range []string{
		`authors: ruleValues(form.elements.authors.value)`,
		`exclude_keywords: ruleValues(form.elements.exclude_keywords.value)`,
	} {
		if strings.Contains(contents, forbiddenPayload) {
			t.Errorf("embedded subscription request still sends unsupported field %q", forbiddenPayload)
		}
	}
}

func TestEmbeddedFrontendRequiresPasswordConfirmationAndComposition(t *testing.T) {
	script, err := fs.ReadFile(appFiles, "assets/app.js")
	if err != nil {
		t.Fatalf("read embedded app script: %v", err)
	}

	contents := string(script)
	for _, fragment := range []string{
		`name="confirm_password"`,
		`confirmPassword.value !== passwordValue`,
		`/\p{L}/u.test(passwordValue)`,
		`/\p{N}/u.test(passwordValue)`,
		`两次输入的密码不一致`,
	} {
		if !strings.Contains(contents, fragment) {
			t.Errorf("embedded registration form is missing %q", fragment)
		}
	}
	for _, internalCopy := range []string{
		"API、MySQL 与 Redis",
		"JWT 鉴权已完成",
		"Worker 当前仅提供运行心跳",
		"能力进度",
	} {
		if strings.Contains(contents, internalCopy) {
			t.Errorf("embedded user interface exposes internal progress copy %q", internalCopy)
		}
	}
}

func TestEmbeddedShellUsesVersionedFrontendAssets(t *testing.T) {
	contents := string(indexHTML)
	for _, asset := range []string{
		`/assets/app.css?v=m3-ui-2`,
		`/assets/app.js?v=m3-ui-2`,
	} {
		if !strings.Contains(contents, asset) {
			t.Errorf("embedded application shell is missing versioned asset %q", asset)
		}
	}
}

func TestEmbeddedFrontendIncludesResponsiveSubscriptionStyles(t *testing.T) {
	stylesheet, err := fs.ReadFile(appFiles, "assets/app.css")
	if err != nil {
		t.Fatalf("read embedded app stylesheet: %v", err)
	}

	contents := string(stylesheet)
	for _, selector := range []string{
		".subscription-card",
		".subscription-modal",
		".category-choice",
		".mobile-bar",
	} {
		if !strings.Contains(contents, selector) {
			t.Errorf("embedded subscription stylesheet is missing %q", selector)
		}
	}
}

func TestEmbeddedFrontendIncludesMatchedPaperWorkspace(t *testing.T) {
	script, err := fs.ReadFile(appFiles, "assets/app.js")
	if err != nil {
		t.Fatalf("read embedded app script: %v", err)
	}
	for _, fragment := range []string{
		`href="/papers"`,
		"apiRequest(`/papers?",
		"apiRequest(`/papers/${",
		`data-filter-paper-subscription`,
		`matched_keywords`,
		`rel="noopener noreferrer"`,
	} {
		if !strings.Contains(string(script), fragment) {
			t.Errorf("embedded matched-paper workspace is missing %q", fragment)
		}
	}

	stylesheet, err := fs.ReadFile(appFiles, "assets/app.css")
	if err != nil {
		t.Fatalf("read embedded app stylesheet: %v", err)
	}
	for _, selector := range []string{
		".matched-paper-card", ".paper-detail-modal", ".paper-match-summary", ".dashboard-paper",
	} {
		if !strings.Contains(string(stylesheet), selector) {
			t.Errorf("embedded matched-paper stylesheet is missing %q", selector)
		}
	}
}
