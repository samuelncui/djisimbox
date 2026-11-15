package setup

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// withoutEcho runs read with terminal echo disabled when in is a terminal.
// Echo is restored even when read fails; other inputs, such as pipes, are read as they are.
func withoutEcho(in io.Reader, out io.Writer, read func() (string, error)) (string, error) {
	// Only a terminal has echo to disable.
	file, ok := in.(*os.File)
	if !ok {
		return read()
	}
	saved, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	if err != nil {
		return read()
	}

	// Hide typed characters until the line has been read.
	hidden := *saved
	hidden.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(file.Fd()), unix.TCSETS, &hidden); err != nil {
		return "", fmt.Errorf("disable terminal echo: %w", err)
	}
	defer unix.IoctlSetTermios(int(file.Fd()), unix.TCSETS, saved)
	defer fmt.Fprintln(out)
	return read()
}
