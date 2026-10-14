package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogFileTurnsOver(t *testing.T) {
	dir := t.TempDir()
	defer log.SetOutput(os.Stderr)
	keepLog(dir)
	line := strings.Repeat("x", 1000)
	for i := 0; i < 600; i++ {
		log.Print(line)
	}
	cur, _ := os.Stat(filepath.Join(dir, "petasos.log"))
	old, err := os.Stat(filepath.Join(dir, "petasos.log.1"))
	if err != nil || cur.Size() > logLimit || old.Size() > logLimit || cur.Size() == 0 {
		t.Fatalf("log sizes: %v %v %v", cur, old, err)
	}
}
