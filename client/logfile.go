package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The system journal lives in memory and is gone after a power cycle, so the device also keeps
// its own log in dir: petasos.log, plus the one before it as petasos.log.1, each up to logLimit.
const logLimit = 512 << 10

type logFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

func keepLog(dir string) {
	l := &logFile{path: filepath.Join(dir, "petasos.log")}
	if err := l.open(); err != nil {
		log.Printf("can't keep a log in %s: %v", dir, err)
		return
	}
	log.SetOutput(io.MultiWriter(os.Stderr, l))
	log.Printf("petasos %s starting", version)
}

func (l *logFile) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.f, l.size = f, info.Size()
	return nil
}

func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return len(p), nil
	}
	if l.size+int64(len(p)) > logLimit {
		l.f.Close()
		l.f = nil
		_ = os.Rename(l.path, l.path+".1")
		if err := l.open(); err != nil {
			return len(p), nil // the journal still has it
		}
	}
	line := append([]byte(time.Now().Format("2006-01-02 15:04:05 ")), p...)
	n, _ := l.f.Write(line)
	l.size += int64(n)
	return len(p), nil
}
