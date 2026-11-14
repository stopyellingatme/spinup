package compat

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// PinState identifies the relationship between a CLI and lock version.
type PinState int

const (
	// PinUnknown means a version is not a release semantic version.
	PinUnknown PinState = iota
	// PinMatch means the CLI can work with the lock version.
	PinMatch
	// PinCLIOlder means the CLI predates the lock version.
	PinCLIOlder
	// PinCLINewer means the CLI is materially newer than the lock version.
	PinCLINewer
)

// PinSlack is the prerelease counter gap that reports a newer CLI.
const PinSlack = 20

// PinAdvice reports the state and, when needed, a user action.
type PinAdvice struct {
	State        PinState
	Message, Try string
}

// CheckPin compares a CLI version with the version that wrote spinup.lock.
func CheckPin(cli, pin string) PinAdvice {
	cliVersion, err := ParseVersion(cli)
	if err != nil || isPseudoVersion(cliVersion) {
		return PinAdvice{State: PinUnknown}
	}
	pinVersion, err := ParseVersion(pin)
	if err != nil || isPseudoVersion(pinVersion) {
		return PinAdvice{State: PinUnknown}
	}

	state := PinMatch
	if cliVersion.Major != pinVersion.Major || cliVersion.Minor != pinVersion.Minor {
		state = pinCompareState(cliVersion.Compare(pinVersion))
	} else if cliVersion.Pre != "" && pinVersion.Pre != "" && cliVersion.Patch == pinVersion.Patch {
		cliPrefix, cliNumber, cliOK := prereleaseCounter(cliVersion.Pre)
		pinPrefix, pinNumber, pinOK := prereleaseCounter(pinVersion.Pre)
		switch {
		case cliOK && pinOK && cliPrefix == pinPrefix:
			switch {
			case cliNumber < pinNumber:
				state = PinCLIOlder
			case cliNumber >= pinNumber+PinSlack:
				state = PinCLINewer
			}
		default:
			state = pinCompareState(cliVersion.Compare(pinVersion))
		}
	}

	return pinAdvice(state, versionText(cliVersion), versionText(pinVersion))
}

func pinCompareState(compare int) PinState {
	if compare < 0 {
		return PinCLIOlder
	}
	if compare > 0 {
		return PinCLINewer
	}
	return PinMatch
}

func prereleaseCounter(pre string) (string, int, bool) {
	index := strings.LastIndex(pre, ".")
	if index < 1 {
		return "", 0, false
	}
	number, err := strconv.Atoi(pre[index+1:])
	if err != nil {
		return "", 0, false
	}
	return pre[:index], number, true
}

// pseudoVersion matches the prerelease of each Go pseudo-version form:
// vX.0.0-yyyymmddhhmmss-hash, vX.Y.Z-pre.0.yyyymmddhhmmss-hash, and vX.Y.Z-0.yyyymmddhhmmss-hash.
var pseudoVersion = regexp.MustCompile(`(^|\.)[0-9]{14}-[0-9A-Za-z]+$`)

func isPseudoVersion(v Version) bool {
	return pseudoVersion.MatchString(v.Pre)
}

func pinAdvice(state PinState, cli, pin string) PinAdvice {
	switch state {
	case PinCLIOlder:
		return PinAdvice{State: state, Message: fmt.Sprintf("spinup %s is older than %s, the version that wrote spinup.lock", cli, pin), Try: "spinup self-update"}
	case PinCLINewer:
		return PinAdvice{State: state, Message: fmt.Sprintf("spinup %s is newer than %s, the version that wrote spinup.lock", cli, pin), Try: "spinup upgrade --plan"}
	default:
		return PinAdvice{State: state}
	}
}

func versionText(v Version) string { return strings.TrimPrefix(v.String(), "v") }
