package eval

import (
	"fmt"
	"strconv"
	"strings"
)

// maxPolicy is the highest CMP number this implementation knows about. Policies
// are identified by number rather than by name because that is how the language
// refers to them, and because `if(POLICY CMP0135)` is how a project asks
// whether the running CMake is new enough to have a given behaviour at all.
const maxPolicy = 195

// policyIntroduced maps a policy to the CMake version that introduced it. A
// cmake_minimum_required(VERSION x) sets every policy introduced at or before
// x to NEW and leaves the rest at their default.
//
// Only the entries that projects actually branch on are listed; anything absent
// is treated as introduced in 3.0, which is the floor this implementation
// supports.
var policyIntroduced = map[int]string{
	11: "3.0", 22: "3.0", 28: "3.0", 38: "3.0", 41: "3.0", 45: "3.0",
	46: "3.0", 47: "3.0", 48: "3.0", 49: "3.0", 50: "3.0", 51: "3.0",
	53: "3.1", 54: "3.1", 55: "3.2", 56: "3.2", 57: "3.3", 58: "3.3",
	59: "3.3", 60: "3.3", 61: "3.4", 62: "3.4", 63: "3.4", 64: "3.4",
	65: "3.4", 66: "3.5", 67: "3.6", 68: "3.7", 69: "3.8", 70: "3.8",
	71: "3.10", 72: "3.11", 73: "3.12", 74: "3.12", 75: "3.12", 76: "3.13",
	77: "3.13", 78: "3.14", 79: "3.14", 80: "3.14", 81: "3.14", 82: "3.14",
	83: "3.14", 84: "3.14", 85: "3.15", 86: "3.15", 87: "3.15", 88: "3.16",
	89: "3.16", 90: "3.17", 91: "3.17", 92: "3.17", 93: "3.17", 94: "3.18",
	95: "3.18", 96: "3.18", 97: "3.18", 98: "3.19", 99: "3.19", 100: "3.19",
	101: "3.20", 102: "3.20", 103: "3.20", 104: "3.20", 105: "3.20",
	106: "3.20", 107: "3.20", 108: "3.20", 109: "3.20", 110: "3.21",
	111: "3.21", 112: "3.21", 113: "3.21", 114: "3.21", 115: "3.21",
	116: "3.22", 117: "3.22", 118: "3.23", 119: "3.23", 120: "3.24",
	121: "3.24", 122: "3.24", 123: "3.24", 124: "3.24", 125: "3.24",
	126: "3.24", 127: "3.24", 128: "3.25", 129: "3.25", 130: "3.25",
	131: "3.25", 132: "3.26", 133: "3.26", 134: "3.27", 135: "3.24",
	136: "3.24", 137: "3.27", 138: "3.27", 139: "3.27", 140: "3.28",
	141: "3.28", 142: "3.28", 143: "3.28", 144: "3.28", 145: "3.29",
	146: "3.29", 147: "3.29", 148: "3.29", 149: "3.29", 150: "3.30",
	151: "3.30", 152: "3.30", 153: "3.30", 154: "3.30", 155: "3.30",
	156: "3.31", 157: "3.31", 158: "3.31", 159: "3.31", 160: "3.31",
	161: "3.31", 162: "3.31", 163: "3.31", 164: "3.31", 165: "3.31",
	166: "3.31", 167: "3.31", 168: "3.31", 169: "3.31", 170: "3.31",
	171: "3.31", 172: "3.31", 173: "3.31", 174: "3.31", 175: "3.31",
	176: "3.31", 177: "3.31", 178: "3.31", 179: "3.31", 180: "3.31",
}

// knownPolicy reports whether a CMP#### string names a policy this
// implementation recognises. It is what `if(POLICY CMP0074)` consults.
func knownPolicy(name string) bool {
	n, ok := policyNumber(name)
	return ok && n >= 0 && n <= maxPolicy
}

// policyNumber parses a "CMP0123" string into its number.
func policyNumber(name string) (int, bool) {
	if len(name) != 7 || !strings.EqualFold(name[:3], "CMP") {
		return 0, false
	}
	n, err := strconv.Atoi(name[3:])
	if err != nil {
		return 0, false
	}
	return n, true
}

// policyName renders a policy number back to its canonical string.
func policyName(n int) string { return fmt.Sprintf("CMP%04d", n) }

// SetPolicyVersion applies the policy defaults implied by a
// cmake_minimum_required(VERSION v): everything introduced at or before v
// becomes NEW.
func (s *State) SetPolicyVersion(v string) {
	for n := 0; n <= maxPolicy; n++ {
		intro, ok := policyIntroduced[n]
		if !ok {
			intro = "3.0"
		}
		if CompareVersions(intro, v) <= 0 {
			s.Policies[policyName(n)] = "NEW"
		}
	}
}

// PolicyGet returns the setting of a policy: "NEW", "OLD", or "" when the
// project has not spoken to it.
func (s *State) PolicyGet(name string) string {
	return s.Policies[strings.ToUpper(name)]
}

// PolicySet records a policy setting.
func (s *State) PolicySet(name, value string) {
	s.Policies[strings.ToUpper(name)] = value
}

// PushPolicyScope saves the current policy settings, as cmake_policy(PUSH) and
// every function call boundary do.
func (s *State) PushPolicyScope() {
	snapshot := make(map[string]string, len(s.Policies))
	for k, v := range s.Policies {
		snapshot[k] = v
	}
	s.PolicyStack = append(s.PolicyStack, snapshot)
}

// PopPolicyScope restores the policy settings saved by the matching push.
func (s *State) PopPolicyScope() {
	if len(s.PolicyStack) == 0 {
		return
	}
	s.Policies = s.PolicyStack[len(s.PolicyStack)-1]
	s.PolicyStack = s.PolicyStack[:len(s.PolicyStack)-1]
}

// oldBehaviorRemovedBefore is the CMake version below which a policy's OLD
// behaviour no longer exists. CMake 4 dropped compatibility with projects
// written for CMake 3.4 and earlier, so cmake_policy(SET <old policy> OLD) is
// an error rather than a warning for anything introduced before this.
const oldBehaviorRemovedBefore = "3.5"

// OldBehaviorAvailable reports whether a policy may still be set to OLD.
func OldBehaviorAvailable(name string) (available bool, introduced string) {
	n, ok := policyNumber(name)
	if !ok {
		return false, ""
	}
	intro, known := policyIntroduced[n]
	if !known {
		intro = "3.0"
	}
	return CompareVersions(intro, oldBehaviorRemovedBefore) >= 0, intro
}
