package eval

import (
	"bytes"
	"context"
	"io"
	"strconv"
	"strings"
)

func init() {
	register("execute_process", cmdExecuteProcess)
	register("try_compile", cmdTryCompile)
	register("try_run", cmdTryRun)
}

// Command is one process invocation requested during configure.
type Command struct {
	Argv   []string
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Runner executes a command. The configure phase needs one because
// execute_process and try_compile are not decidable without running something;
// a State without a Runner reports those commands as unavailable rather than
// pretending they succeeded.
type Runner interface {
	Run(ctx context.Context, cmd Command) (exitCode int, err error)
}

// cmdExecuteProcess runs one or more commands, optionally piped together.
func cmdExecuteProcess(ctx context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if e.state.Runner == nil {
		return e.fatalf("execute_process requires a process runner, which this configuration does not provide")
	}

	var commands [][]string
	var current []string
	workDir := e.state.Dir().Binary
	outVar, errVar, resVar, resultsVar := "", "", "", ""
	stripOut, stripErr := false, false
	inputFile, outputFile, errorFile := "", "", ""
	mergeOutput := false
	quietOut, quietErr := false, false
	var timeout string

	keyword := ""
	flush := func() {
		if len(current) > 0 {
			commands = append(commands, current)
			current = nil
		}
	}
	for _, a := range v {
		switch a {
		case "COMMAND":
			flush()
			keyword = "COMMAND"
			continue
		case "WORKING_DIRECTORY", "TIMEOUT", "RESULT_VARIABLE", "RESULTS_VARIABLE",
			"OUTPUT_VARIABLE", "ERROR_VARIABLE", "INPUT_FILE", "OUTPUT_FILE",
			"ERROR_FILE", "COMMAND_ECHO", "ENCODING":
			flush()
			keyword = a
			continue
		case "OUTPUT_QUIET":
			quietOut = true
			keyword = ""
			continue
		case "ERROR_QUIET":
			quietErr = true
			keyword = ""
			continue
		case "OUTPUT_STRIP_TRAILING_WHITESPACE":
			stripOut = true
			keyword = ""
			continue
		case "ERROR_STRIP_TRAILING_WHITESPACE":
			stripErr = true
			keyword = ""
			continue
		case "ECHO_OUTPUT_VARIABLE", "ECHO_ERROR_VARIABLE", "COMMAND_ERROR_IS_FATAL":
			keyword = a
			continue
		}
		switch keyword {
		case "COMMAND":
			current = append(current, a)
		case "WORKING_DIRECTORY":
			workDir = e.state.absPath(a)
			keyword = ""
		case "TIMEOUT":
			timeout = a
			keyword = ""
		case "RESULT_VARIABLE":
			resVar = a
			keyword = ""
		case "RESULTS_VARIABLE":
			resultsVar = a
			keyword = ""
		case "OUTPUT_VARIABLE":
			outVar = a
			keyword = ""
		case "ERROR_VARIABLE":
			errVar = a
			keyword = ""
		case "INPUT_FILE":
			inputFile = e.state.absPath(a)
			keyword = ""
		case "OUTPUT_FILE":
			outputFile = e.state.absPath(a)
			keyword = ""
		case "ERROR_FILE":
			errorFile = e.state.absPath(a)
			keyword = ""
		case "COMMAND_ERROR_IS_FATAL":
			keyword = ""
		}
	}
	flush()
	_ = timeout

	if len(commands) == 0 {
		return e.fatalf("execute_process given no COMMAND")
	}
	// ERROR_VARIABLE naming the same variable as OUTPUT_VARIABLE is how a
	// caller asks for the two streams to be merged.
	if outVar != "" && outVar == errVar {
		mergeOutput = true
	}

	var stdout, stderr bytes.Buffer
	var codes []string
	var last int

	// Commands are piped: each one's output becomes the next one's input, and
	// only the last one's output reaches the caller.
	var pipeIn io.Reader
	if inputFile != "" {
		data, err := e.fs.ReadFile(inputFile)
		if err != nil {
			return e.fatalf("execute_process could not read INPUT_FILE %s", inputFile)
		}
		pipeIn = bytes.NewReader(data)
	}

	for i, argv := range commands {
		var out bytes.Buffer
		cmd := Command{
			Argv:   argv,
			Dir:    workDir,
			Env:    e.state.envSlice(),
			Stdin:  pipeIn,
			Stdout: &out,
			Stderr: &stderr,
		}
		if mergeOutput {
			cmd.Stderr = &out
		}
		code, err := e.state.Runner.Run(ctx, cmd)
		if err != nil && code == 0 {
			// A command that could not start at all reports its message where
			// a caller checking RESULT_VARIABLE will see it.
			codes = append(codes, err.Error())
			last = -1
			stderr.WriteString(err.Error())
			break
		}
		codes = append(codes, strconv.Itoa(code))
		last = code
		if i == len(commands)-1 {
			stdout = out
		} else {
			pipeIn = bytes.NewReader(out.Bytes())
		}
	}

	outText, errText := stdout.String(), stderr.String()
	if stripOut {
		outText = strings.TrimRight(outText, " \t\r\n")
	}
	if stripErr {
		errText = strings.TrimRight(errText, " \t\r\n")
	}
	if outputFile != "" {
		_ = e.fs.WriteFile(outputFile, []byte(outText))
	}
	if errorFile != "" {
		_ = e.fs.WriteFile(errorFile, []byte(errText))
	}
	if outVar != "" && !quietOut {
		e.state.SetVar(outVar, outText)
	}
	if errVar != "" && errVar != outVar && !quietErr {
		e.state.SetVar(errVar, errText)
	}
	if resVar != "" {
		e.state.SetVar(resVar, strconv.Itoa(last))
	}
	if resultsVar != "" {
		e.state.SetVar(resultsVar, JoinList(codes))
	}
	return nil
}

// envSlice renders the environment table back into the KEY=VALUE form a
// process runner expects.
func (s *State) envSlice() []string {
	out := make([]string, 0, len(s.Env))
	for k, v := range s.Env {
		out = append(out, k+"="+v)
	}
	return out
}

// cmdTryCompile reports failure rather than a false success.
//
// A real try_compile writes a small project, configures it, and builds it. That
// requires a working generator and toolchain, which is a build-phase concern.
// Returning TRUE here would be worse than returning FALSE: a project would
// enable a feature its compiler does not have and fail later with a confusing
// error, so the result is FALSE and the reason is recorded where the project
// can see it.
func cmdTryCompile(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) == 0 {
		return e.fatalf("try_compile called with incorrect number of arguments")
	}
	e.state.SetVar(v[0], "FALSE")
	for i := 1; i+1 < len(v); i++ {
		if v[i] == "OUTPUT_VARIABLE" {
			e.state.SetVar(v[i+1], "try_compile is not implemented by this CMake implementation")
		}
	}
	e.state.Unsupported = append(e.state.Unsupported, "try_compile")
	return nil
}

func cmdTryRun(_ context.Context, e *evaluator, args []Arg) error {
	v := Args(args)
	if len(v) < 2 {
		return e.fatalf("try_run called with incorrect number of arguments")
	}
	e.state.SetVar(v[0], "FAILED_TO_RUN")
	e.state.SetVar(v[1], "FALSE")
	e.state.Unsupported = append(e.state.Unsupported, "try_run")
	return nil
}
