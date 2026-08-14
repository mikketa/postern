package pool

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseProxy takes a proxy the way a provider hands it over and returns the URL
// the browser wants.
//
// Residential providers do not sell URLs. They sell lines, and almost always
// this line:
//
//	gate.example.com:8000:user-session-42:hunter2
//
// Retyping a hundred of those into http://user:pass@host:port is both tedious
// and the sort of thing that goes wrong quietly — a mistyped password is a
// proxy that 407s, which used to look like nothing at all and now looks like an
// identity that fails three times and quarantines itself. Better to take the
// line as sold.
//
// A URL is passed through, so a provider that does sell URLs is no worse off.
func ParseProxy(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", nil
	}
	if strings.Contains(text, "://") {
		return text, nil
	}

	parts := strings.Split(text, ":")
	switch len(parts) {
	case 2:
		// host:port
		if err := checkPort(parts[1]); err != nil {
			return "", err
		}
		return "http://" + text, nil
	case 4:
		// host:port:user:pass — the form nearly every provider ships.
		host, port, user, pass := parts[0], parts[1], parts[2], parts[3]
		if err := checkPort(port); err != nil {
			return "", err
		}
		if host == "" || user == "" || pass == "" {
			return "", fmt.Errorf("proxy %q: host, user and password must all be there", text)
		}
		return fmt.Sprintf("http://%s:%s@%s:%s", user, pass, host, port), nil
	default:
		return "", fmt.Errorf("proxy %q: expected host:port, host:port:user:pass, "+
			"or a full url like http://user:pass@host:port", text)
	}
}

func checkPort(port string) error {
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("proxy port %q is not a port number", port)
	}
	return nil
}

// CheckFleet reports what is wrong with a set of identities before any of them
// is used.
//
// Both of these are silent failures otherwise, and both defeat the point of
// running a fleet at all: identities that share an exit are one identity
// wearing several profiles, and a fleet with no proxies at all is one identity
// wearing all of them. Neither shows up as an error — they show up weeks later
// as a pool that mysteriously stops producing tokens.
func CheckFleet(identities []*Identity) []string {
	var problems []string

	exits := map[string][]string{}
	bare := 0
	for _, identity := range identities {
		if identity.Proxy == "" {
			bare++
			continue
		}
		exits[identity.Proxy] = append(exits[identity.Proxy], identity.Name)
	}

	for exit, names := range exits {
		if len(names) > 1 {
			problems = append(problems, fmt.Sprintf(
				"%s go out through the same proxy, so they are one identity in %d profiles",
				strings.Join(names, ", "), len(names)))
		}
		_ = exit
	}

	if bare > 1 {
		problems = append(problems, fmt.Sprintf(
			"%d identities have no proxy, so they all share this machine's address — "+
				"the fleet will wear out as fast as a single identity would", bare))
	}

	return problems
}
