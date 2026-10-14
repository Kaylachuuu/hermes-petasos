package main

import (
	"errors"
	"os"
)

// On Windows the program only runs as the virtual Petasos, which has no input devices and
// doesn't update itself (the app brings its own copy).

func grabInput(*os.File) error { return errors.New("not on Windows") }

func execReplace(string, []string) error { return errors.New("not on Windows") }
