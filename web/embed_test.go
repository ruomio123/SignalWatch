package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedBuildReferencesExistingHashedAssets(t *testing.T) {
	paths := regexp.MustCompile(`(?:src|href)="(/assets/[^"?]+)"`).FindAllStringSubmatch(string(indexHTML), -1)
	if len(paths) < 2 {
		t.Fatal("build must include module script and CSS")
	}
	for _, p := range paths {
		body, err := fs.ReadFile(appFiles, strings.TrimPrefix(p[1], "/"))
		if err != nil || len(body) == 0 {
			t.Fatalf("missing asset %s: %v", p[1], err)
		}
	}
	if !strings.Contains(string(indexHTML), `type="module"`) {
		t.Fatal("missing module entry point")
	}
}
