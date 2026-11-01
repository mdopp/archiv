package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"
)

// terminal is a pseudo-terminal pair, made with the standard library
// alone: signalbackup-tools reads its passphrase only from a terminal
// (tcgetattr on stdin, echo off), so the passphrase is typed into one
// instead of going on its command line, where /proc/<pid>/cmdline shows it.
type terminal struct {
	master, slave *os.File
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

func neuesTerminal() (*terminal, error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var aus int32
	if err := ioctl(m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&aus))); err != nil {
		m.Close()
		return nil, fmt.Errorf("unlockpt: %w", err)
	}
	var n uint32
	if err := ioctl(m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); err != nil {
		m.Close()
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	s, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, err
	}
	return &terminal{master: m, slave: s}, nil
}

func (t *terminal) schliessen() {
	if t.slave != nil {
		t.slave.Close()
	}
	t.master.Close()
}

// echoAus reports whether the program on the terminal has switched echo
// off, i.e. is now asking for the passphrase. On Linux a TCGETS on the
// master answers with the slave's settings.
func (t *terminal) echoAus() (bool, error) {
	var tio syscall.Termios
	if err := ioctl(t.master.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&tio))); err != nil {
		return false, err
	}
	return tio.Lflag&syscall.ECHO == 0, nil
}

var errKeineFrage = errors.New("das Programm fragte nicht nach der Passphrase")

// tippen waits until the program asks (echo off), then types the line.
// Waiting first means the secret is never echoed back by the terminal.
func (t *terminal) tippen(ctx context.Context, zeile []byte, hoechstens time.Duration) error {
	bis := time.Now().Add(hoechstens)
	for {
		aus, err := t.echoAus()
		if err != nil {
			return err
		}
		if aus {
			_, err := t.master.Write(zeile)
			return err
		}
		if time.Now().After(bis) {
			return errKeineFrage
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
