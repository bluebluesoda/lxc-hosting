package inter

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// IsTTY reports whether stdin is an interactive terminal.
func IsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

var rd *bufio.Reader

func readLine() (string, error) {
	if rd == nil {
		rd = bufio.NewReader(os.Stdin)
	}
	line, err := rd.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Ask prints "label [default] unit: " and returns either the validated input
// or the default on empty input / EOF. Invalid input re-prompts showing the
// validation error.
func Ask(label, def, unit string, validate func(string) error) (string, error) {
	for {
		defStr := ""
		if def != "" {
			defStr = " [" + def + "]"
		}
		fmt.Printf("%s%s%s: ", label, defStr, unit)
		line, err := readLine()
		if err != nil {
			return "", err
		}
		if line == "" {
			return def, nil
		}
		if err := validate(line); err != nil {
			fmt.Printf("  ! %v\n", err)
			continue
		}
		return line, nil
	}
}
