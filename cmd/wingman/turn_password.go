package main

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/Crank-Git/wingman/internal/credentials"
)

func loadTURNPassword(direct, path string) (string, error) {
	if path == "" {
		return direct, nil
	}
	if direct != "" {
		return "", errors.New("conflicting TURN password sources")
	}
	data, err := credentials.ReadRestrictedFile(path)
	if err != nil {
		return "", fmt.Errorf("read TURN password file: %w", err)
	}
	password := strings.TrimSpace(string(data))
	if password == "" || strings.ContainsFunc(password, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", errors.New("invalid TURN password file: require one nonempty token")
	}
	return password, nil
}
