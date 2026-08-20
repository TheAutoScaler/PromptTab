package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func loadPromptTabConfig(path string, config *settings) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	section := ""
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid PromptTab config line %q", scanner.Text())
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		value = strings.Trim(value, "\"")
		switch section + "." + key {
		case ".backend":
			config.backend = value
		case "local.url":
			config.localURL = value
		case "local.name":
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("local.name must not be empty")
			}
			config.localName = value
		case "local.max_tokens":
			parsed, e := strconv.Atoi(value)
			if e != nil || parsed < 1 {
				return fmt.Errorf("local.max_tokens must be positive")
			}
			config.localMaxTokens = parsed
		case "local.temperature":
			parsed, e := strconv.ParseFloat(value, 64)
			if e != nil || parsed < 0 {
				return fmt.Errorf("local.temperature must be non-negative")
			}
			config.localTemperature = parsed
		case "local.timeout_ms":
			parsed, e := strconv.Atoi(value)
			if e != nil || parsed < 1 {
				return fmt.Errorf("local.timeout_ms must be positive")
			}
			config.localTimeout = time.Duration(parsed) * time.Millisecond
		default:
			return fmt.Errorf("unknown PromptTab config key %s.%s", section, key)
		}
	}
	return scanner.Err()
}
