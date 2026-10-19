package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTheSentLogEndsWithTheNewestLines(t *testing.T) {
	was := virtual
	virtual = true // no device checks here
	defer func() { virtual = was }()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "petasos.log.1"), bytes.Repeat([]byte("old\n"), sentLogLimit/4), 0o644)
	os.WriteFile(filepath.Join(dir, "petasos.log"), []byte("the car's call button\n"), 0o644)
	report := logReport(dir)
	if len(report) > sentLogLimit+100 {
		t.Fatalf("report is %d bytes", len(report))
	}
	if !bytes.HasSuffix(report, []byte("the car's call button\n")) {
		t.Fatalf("report doesn't end with the newest line: %q", report[len(report)-40:])
	}
}
