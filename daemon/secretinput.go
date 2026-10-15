package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// readSecretLine reads one line that is a secret. At a terminal it is typed
// with echo off (withEchoOff), so it never appears on screen or in a
// recording; piped in, it is read as it comes.
func readSecretLine(in io.Reader, out io.Writer, prompt string) (string, error) {
	read := func() (string, error) {
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("reading the value: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	f, ok := in.(*os.File)
	if !ok || !isTerminal(f) {
		return read()
	}
	_, _ = fmt.Fprint(out, prompt)
	var line string
	err := withEchoOff(f, func() error {
		var err error
		line, err = read()
		return err
	})
	_, _ = fmt.Fprintln(out)
	return line, err
}
