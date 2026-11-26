package store

import (
	"errors"
	"os"
)

var ErrLocked = errors.New("another whatsapp-mcp instance is already running against this store directory")

type lockState int

const (
	lockStateUnlocked lockState = iota
	lockStateLocked
)

type FileLock struct {
	path  string
	f     *os.File
	state lockState
}
