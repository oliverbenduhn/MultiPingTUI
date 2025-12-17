package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type UserSettings struct {
	Hosts []string         `json:"hosts"`
	View  UserViewSettings `json:"view"`
}

type UserViewSettings struct {
	Filter FilterMode      `json:"filter"`
	Sort   SortMode        `json:"sort"`
	Rate   UpdateRate      `json:"rate"`
	Cols   []int           `json:"cols"`
	Hidden map[string]bool `json:"hidden"`
}

func DefaultUserSettings() UserSettings {
	return UserSettings{
		Hosts: []string{"localhost", "www.github.com"},
		View: UserViewSettings{
			Filter: FilterSmart,
			Sort:   SortByIP,
			Rate:   UpdateRate1s,
			Cols:   []int{1, 2, 3, 4, 5, 6},
			Hidden: map[string]bool{},
		},
	}
}

func ValidateUserSettings(settings UserSettings) error {
	for i, h := range settings.Hosts {
		if strings.TrimSpace(h) == "" {
			return fmt.Errorf("hosts[%d] is empty", i)
		}
	}
	if !validFilterMode(settings.View.Filter) {
		return fmt.Errorf("view.filter is invalid: %d", settings.View.Filter)
	}
	if !validSortMode(settings.View.Sort) {
		return fmt.Errorf("view.sort is invalid: %d", settings.View.Sort)
	}
	if !validUpdateRate(settings.View.Rate) {
		return fmt.Errorf("view.rate is invalid: %d", settings.View.Rate)
	}
	seen := make(map[int]bool, 6)
	for i, c := range settings.View.Cols {
		if c < 1 || c > 6 {
			return fmt.Errorf("view.cols[%d] out of range: %d", i, c)
		}
		if seen[c] {
			return fmt.Errorf("view.cols contains duplicate: %d", c)
		}
		seen[c] = true
	}
	for k := range settings.View.Hidden {
		if strings.TrimSpace(k) == "" {
			// ignore legacy empty key
			continue
		}
	}
	return nil
}

func userSettingsPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("MPING_CONFIG")); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mping", "config.yaml"), nil
}

func LoadUserSettings() (UserSettings, error) {
	var settings UserSettings
	path, err := userSettingsPath()
	if err != nil {
		return settings, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return settings, nil
		}
		return settings, err
	}
	parsed, err := parseUserSettings(b)
	if err != nil {
		return settings, err
	}
	if err := ValidateUserSettings(parsed); err != nil {
		return settings, err
	}
	return parsed, nil
}

func SaveUserSettings(settings UserSettings) error {
	path, err := userSettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out := marshalUserSettingsYAML(settings)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseUserSettings(b []byte) (UserSettings, error) {
	var settings UserSettings
	raw := bytes.TrimSpace(b)
	if len(raw) == 0 {
		return settings, nil
	}

	// YAML is a superset of JSON; accept JSON directly.
	if raw[0] == '{' || raw[0] == '[' {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return settings, err
		}
		return settings, nil
	}

	// Very small YAML subset parser for our own format.
	sc := bufio.NewScanner(bytes.NewReader(raw))
	section := ""
	subsection := ""

	for sc.Scan() {
		line := sc.Text()
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))
		trim := strings.TrimSpace(line)

		if indent == 0 {
			// Section header like "hosts:" or inline empty like "hosts: []"
			if strings.HasSuffix(trim, ":") {
				section = strings.TrimSuffix(trim, ":")
				subsection = ""
				continue
			}
			k, v, ok := splitYAMLKeyValue(trim)
			if ok {
				if k == "hosts" && strings.TrimSpace(v) == "[]" {
					settings.Hosts = nil
					section = "hosts"
					subsection = ""
					continue
				}
				if k == "view" && strings.TrimSpace(v) == "{}" {
					section = "view"
					subsection = ""
					continue
				}
			}
		}

		switch section {
		case "hosts":
			// - "host"
			if strings.HasPrefix(trim, "- ") {
				v := strings.TrimSpace(strings.TrimPrefix(trim, "- "))
				if s, ok := parseYAMLString(v); ok {
					settings.Hosts = append(settings.Hosts, s)
				} else if v != "" {
					settings.Hosts = append(settings.Hosts, v)
				}
			}

		case "view":
			if indent == 2 && strings.HasSuffix(trim, ":") {
				subsection = strings.TrimSuffix(trim, ":")
				continue
			}
			if subsection == "hidden" {
				//   "key": true
				k, v, ok := splitYAMLKeyValue(trim)
				if !ok {
					continue
				}
				key, _ := parseYAMLString(k)
				if key == "" {
					key = strings.TrimSpace(k)
				}
				bv, ok := parseYAMLBool(v)
				if !ok {
					continue
				}
				if settings.View.Hidden == nil {
					settings.View.Hidden = make(map[string]bool)
				}
				settings.View.Hidden[key] = bv
				continue
			}

			k, v, ok := splitYAMLKeyValue(trim)
			if !ok {
				continue
			}
			switch k {
			case "filter":
				if n, ok := parseYAMLInt(v); ok {
					settings.View.Filter = FilterMode(n)
				}
			case "sort":
				if n, ok := parseYAMLInt(v); ok {
					settings.View.Sort = SortMode(n)
				}
			case "rate":
				if n, ok := parseYAMLInt(v); ok {
					settings.View.Rate = UpdateRate(n)
				}
			case "cols":
				settings.View.Cols = parseYAMLIntList(v)
			case "hidden":
				// handled via subsection, but accept inline object as JSON/YAML-flow ("{}" or {"a":true}).
				if strings.TrimSpace(v) == "{}" {
					settings.View.Hidden = nil
					continue
				}
				if strings.HasPrefix(strings.TrimSpace(v), "{") {
					var m map[string]bool
					_ = json.Unmarshal([]byte(v), &m)
					if len(m) > 0 {
						settings.View.Hidden = m
					}
				}
			}
		}
	}

	if err := sc.Err(); err != nil {
		return settings, err
	}
	return settings, nil
}

func marshalUserSettingsYAML(settings UserSettings) []byte {
	var b strings.Builder
	b.WriteString("# mping config\n")
	b.WriteString("#\n")
	b.WriteString("# This file is automatically loaded on startup and saved on exit.\n")
	b.WriteString("# Override the location via the environment variable: MPING_CONFIG\n")
	b.WriteString("#\n")
	b.WriteString("# hosts:\n")
	b.WriteString("#   - A list of targets to probe.\n")
	b.WriteString("#   - Supports: hostnames, IPs, CIDR (e.g. \"192.168.1.0/24\"), and tcp://host:port.\n")
	b.WriteString("#\n")
	b.WriteString("# view:\n")
	b.WriteString("#   - Initial TUI/Web view state.\n")
	b.WriteString("#   filter:\n")
	b.WriteString("#     0=All, 1=Smart (online or ever-seen), 2=Online, 3=Offline\n")
	b.WriteString("#   sort:\n")
	b.WriteString("#     0=Name, 1=Status, 2=RTT, 3=Last Seen, 4=IP\n")
	b.WriteString("#   rate:\n")
	b.WriteString("#     0=100ms, 1=1s, 2=5s, 3=30s\n")
	b.WriteString("#   cols:\n")
	b.WriteString("#     Visible columns (1..6): 1=Status, 2=Name, 3=IP, 4=RTT, 5=Last Reply, 6=Last Loss\n")
	b.WriteString("#   hidden:\n")
	b.WriteString("#     Host key -> true to hide it in the list.\n\n")

	if len(settings.Hosts) == 0 {
		b.WriteString("hosts: []\n")
	} else {
		b.WriteString("hosts:\n")
		for _, h := range settings.Hosts {
			b.WriteString("  - ")
			b.WriteString(yamlQuote(h))
			b.WriteString("\n")
		}
	}

	b.WriteString("\nview:\n")
	b.WriteString(fmt.Sprintf("  filter: %d\n", settings.View.Filter))
	b.WriteString(fmt.Sprintf("  sort: %d\n", settings.View.Sort))
	b.WriteString(fmt.Sprintf("  rate: %d\n", settings.View.Rate))

	b.WriteString("  cols: [")
	for i, c := range settings.View.Cols {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(c))
	}
	b.WriteString("]\n")

	if len(settings.View.Hidden) == 0 {
		b.WriteString("  hidden: {}\n")
	} else {
		b.WriteString("  hidden:\n")
		keys := make([]string, 0, len(settings.View.Hidden))
		for k := range settings.View.Hidden {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			b.WriteString("    ")
			b.WriteString(yamlQuote(k))
			b.WriteString(": ")
			if settings.View.Hidden[k] {
				b.WriteString("true\n")
			} else {
				b.WriteString("false\n")
			}
		}
	}

	return []byte(b.String())
}

func sortStrings(s []string) {
	// tiny insertion sort: no extra deps
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func yamlQuote(s string) string {
	return strconv.Quote(s)
}

func parseYAMLString(v string) (string, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	if (strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"")) || (strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'")) {
		if strings.HasPrefix(v, "'") {
			// YAML single quotes escape by doubling.
			inner := strings.TrimSuffix(strings.TrimPrefix(v, "'"), "'")
			return strings.ReplaceAll(inner, "''", "'"), true
		}
		s, err := strconv.Unquote(v)
		if err == nil {
			return s, true
		}
	}
	return "", false
}

func parseYAMLBool(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

func parseYAMLInt(v string) (int, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseYAMLIntList(v string) []int {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	// Support JSON/YAML flow list: [1,2,3]
	if strings.HasPrefix(v, "[") && strings.HasSuffix(v, "]") {
		var out []int
		inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"))
		if inner == "" {
			return []int{}
		}
		for _, part := range strings.Split(inner, ",") {
			if n, ok := parseYAMLInt(part); ok {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func splitYAMLKeyValue(line string) (string, string, bool) {
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return "", "", false
	}
	k := strings.TrimSpace(line[:idx])
	v := strings.TrimSpace(line[idx+1:])
	// strip quotes around key if present
	if s, ok := parseYAMLString(k); ok {
		k = s
	}
	return k, v, true
}
