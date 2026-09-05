package eval

// project() and the version and policy machinery that surrounds it.

import (
	"context"
	"strconv"
	"strings"
)

func init() {
	register("cmake_minimum_required", cmdCMakeMinimumRequired)
	register("cmake_policy", cmdCMakePolicy)
	register("project", cmdProject)
	register("enable_language", cmdEnableLanguage)
	register("enable_testing", cmdEnableTesting)
}

func cmdCMakeMinimumRequired(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	for i := 0; i < len(vals); i++ {
		if vals[i] != "VERSION" || i+1 >= len(vals) {
			continue
		}
		spec := vals[i+1]
		// The spec may be a range, "3.16...3.28": the lower bound is the
		// minimum required and the upper bound is the newest policy set to
		// apply, so a project can require an old CMake yet opt into new
		// behaviour when a newer one is running.
		low, high := spec, ""
		if k := strings.Index(spec, "..."); k >= 0 {
			low, high = spec[:k], spec[k+3:]
		}
		running := e.state.GetVar("CMAKE_VERSION")
		if CompareVersions(running, low) < 0 {
			return e.fatalf("CMake %s or higher is required.  You are running version %s", low, running)
		}
		apply := low
		if high != "" && CompareVersions(running, high) >= 0 {
			apply = high
		} else if high != "" {
			apply = running
		}
		e.state.SetPolicyVersion(apply)
		e.state.SetVar("CMAKE_MINIMUM_REQUIRED_VERSION", low)
	}
	return nil
}

func cmdCMakePolicy(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return nil
	}
	switch vals[0] {
	case "VERSION":
		if len(vals) > 1 {
			spec := vals[1]
			if k := strings.Index(spec, "..."); k >= 0 {
				spec = spec[:k]
			}
			e.state.SetPolicyVersion(spec)
		}
	case "SET":
		if len(vals) < 3 {
			return e.fatalf("cmake_policy SET requires a policy and a value")
		}
		if !knownPolicy(vals[1]) {
			return e.fatalf("Policy %q is not known to this version of CMake.", vals[1])
		}
		if vals[2] == "OLD" {
			available, intro := OldBehaviorAvailable(vals[1])
			if !available {
				return e.fatalf("Policy %s may not be set to OLD behavior because this version of CMake\n"+
					"  no longer supports it.  The policy was introduced in CMake version %s.0,\n"+
					"  and use of NEW behavior is now required.\n"+
					"\n"+
					"  Please either update your CMakeLists.txt files to conform to the new\n"+
					"  behavior or use an older version of CMake that still supports the old\n"+
					"  behavior.  Run cmake --help-policy %s for more information.",
					vals[1], intro, vals[1])
			}
			e.state.log("DEPRECATION", sprintf(
				"The OLD behavior for policy %s will be removed from a future version\n"+
					"  of CMake.\n"+
					"\n"+
					"  The cmake_policy command may be used to set the policy to NEW behavior for\n"+
					"  this third-party project, or the CMake variable CMAKE_POLICY_DEFAULT_%s may\n"+
					"  be set to NEW to affect all projects.", vals[1], vals[1]))
		}
		e.state.PolicySet(vals[1], vals[2])
	case "GET":
		if len(vals) < 3 {
			return e.fatalf("cmake_policy GET requires a policy and an output variable")
		}
		e.state.SetVar(vals[2], e.state.PolicyGet(vals[1]))
	case "PUSH":
		e.state.PushPolicyScope()
	case "POP":
		e.state.PopPolicyScope()
	}
	return nil
}

func cmdProject(_ context.Context, e *evaluator, args []Arg) error {
	vals := Args(args)
	if len(vals) == 0 {
		return e.fatalf("project called with incorrect number of arguments")
	}
	name := vals[0]
	e.state.SetVar("PROJECT_NAME", name)
	dir := e.state.Dir()
	// CMAKE_PROJECT_NAME names the top-level project for the whole tree. A
	// subproject added with add_subdirectory sets PROJECT_NAME but must leave
	// this one alone: it is how a library tells whether it is being built
	// standalone or as somebody else's dependency.
	topLevel := dir == e.state.RootDir()
	if topLevel {
		e.state.SetVar("CMAKE_PROJECT_NAME", name)
	}
	e.state.SetVar("PROJECT_SOURCE_DIR", dir.Source)
	e.state.SetVar("PROJECT_BINARY_DIR", dir.Binary)
	e.state.SetVar(name+"_SOURCE_DIR", dir.Source)
	e.state.SetVar(name+"_BINARY_DIR", dir.Binary)
	if topLevel {
		e.state.SetVar("CMAKE_PROJECT_SOURCE_DIR", dir.Source)
		e.state.SetVar("CMAKE_PROJECT_BINARY_DIR", dir.Binary)
	}

	// project(name [VERSION v] [DESCRIPTION d] [HOMEPAGE_URL u] [LANGUAGES l...])
	keyword := ""
	var languages []string
	for _, v := range vals[1:] {
		switch v {
		case "VERSION", "DESCRIPTION", "HOMEPAGE_URL", "LANGUAGES":
			keyword = v
			continue
		}
		switch keyword {
		case "VERSION":
			setProjectVersion(e.state, name, v, topLevel)
			keyword = ""
		case "DESCRIPTION":
			e.state.SetVar("PROJECT_DESCRIPTION", v)
			e.state.SetVar(name+"_DESCRIPTION", v)
			keyword = ""
		case "HOMEPAGE_URL":
			e.state.SetVar("PROJECT_HOMEPAGE_URL", v)
			e.state.SetVar(name+"_HOMEPAGE_URL", v)
			keyword = ""
		case "LANGUAGES":
			languages = append(languages, v)
		default:
			// The short form project(name C CXX) lists languages positionally.
			languages = append(languages, v)
		}
	}
	if len(languages) == 0 {
		languages = []string{"C", "CXX"}
	}
	for _, l := range languages {
		if l == "NONE" {
			continue
		}
		e.state.Languages[l] = true
	}
	return nil
}

// setProjectVersion publishes the five variables CMake derives from a version.
func setProjectVersion(s *State, project, version string, topLevel bool) {
	parts := versionComponents(version)
	get := func(i int) string {
		if i < len(parts) {
			return strconv.FormatInt(parts[i], 10)
		}
		return ""
	}
	prefixes := []string{"PROJECT", project}
	if topLevel {
		prefixes = append(prefixes, "CMAKE_PROJECT")
	}
	for _, prefix := range prefixes {
		s.SetVar(prefix+"_VERSION", version)
		s.SetVar(prefix+"_VERSION_MAJOR", get(0))
		s.SetVar(prefix+"_VERSION_MINOR", get(1))
		s.SetVar(prefix+"_VERSION_PATCH", get(2))
		s.SetVar(prefix+"_VERSION_TWEAK", get(3))
	}
}

func cmdEnableLanguage(_ context.Context, e *evaluator, args []Arg) error {
	for _, l := range Args(args) {
		if l == "OPTIONAL" {
			continue
		}
		e.state.Languages[l] = true
	}
	return nil
}

func cmdEnableTesting(_ context.Context, e *evaluator, args []Arg) error {
	e.state.TestingEnabled = true
	return nil
}
