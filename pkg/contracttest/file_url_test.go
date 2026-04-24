package contracttest

import (
	"runtime"
	"strings"
	"testing"
)

func TestSchemaFileURL_CurrentPlatform(t *testing.T) {
	u := schemaFileURL("/tmp/gocell/schema.json")
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path expectation is not meaningful on Windows")
	}
	if u != "file:///tmp/gocell/schema.json" {
		t.Fatalf("schemaFileURL(/tmp/gocell/schema.json) = %q", u)
	}
}

func TestSchemaFileURL_WindowsDrivePath(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive path normalization uses filepath on Windows")
	}
	u := schemaFileURL(`C:\gocell\schema.json`)
	if !strings.HasPrefix(u, "file:///C:/gocell/schema.json") {
		t.Fatalf("schemaFileURL(windows path) = %q", u)
	}
}
