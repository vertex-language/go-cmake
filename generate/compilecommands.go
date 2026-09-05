package generate

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/vertex-language/go-cmake/toolchain"
)

// compile_commands.json is what a language server reads.
//
// clangd, ccls, and every editor that offers completion or diagnostics for C
// and C++ need to know how each file is compiled -- which include paths, which
// definitions, which standard -- and they get it from this file and nowhere
// else. A build tree without one gives an editor no way to parse the project,
// so the symptom is not a build failure but an editor that cannot find a header
// the compiler finds fine.
//
// The format is a flat array of one entry per translation unit, which is why a
// header appears in no entry: it has no compile command of its own.

// CompileCommand is one entry of the database.
type CompileCommand struct {
	// Directory is where the command would be run from. Every relative path in
	// the command is relative to it, so a consumer needs it to resolve them.
	Directory string `json:"directory"`

	// Command is the full command line. The schema also allows an "arguments"
	// array; a single string is used here because it is what CMake writes and
	// what every consumer has therefore been tested against.
	Command string `json:"command"`

	File   string `json:"file"`
	Output string `json:"output,omitempty"`
}

// CompileCommands renders the database for a resolved graph.
type CompileCommands struct {
	Graph     *Graph
	Toolchain *toolchain.Toolchain
	SourceDir string
	BinaryDir string
}

// Entries builds one entry per compiled source, in target declaration order so
// that regenerating an unchanged project produces an unchanged file.
func (c *CompileCommands) Entries() []CompileCommand {
	n := &Ninja{
		Graph:     c.Graph,
		Toolchain: c.Toolchain,
		SourceDir: c.SourceDir,
		BinaryDir: c.BinaryDir,
	}

	var out []CompileCommand
	for _, name := range c.Graph.Order {
		r, ok := c.Graph.Targets[name]
		if !ok || r.Target.Imported {
			continue
		}
		for _, src := range r.Target.Sources {
			abs := r.Target.ResolveSource(src)
			lang := toolchain.LanguageOf(abs)
			if lang == "" {
				// A header listed as a source is there so an IDE shows it in the
				// target. It is not a translation unit and has no command.
				continue
			}
			compiler, ok := c.Toolchain.Compiler(lang)
			if !ok {
				continue
			}
			object := n.objectPath(r.Target, abs)
			out = append(out, CompileCommand{
				Directory: c.BinaryDir,
				Command:   c.commandFor(n, compiler, r, abs, object),
				File:      abs,
				Output:    relativeTo(c.BinaryDir, object),
			})
		}
	}
	return out
}

// commandFor renders the command line for one translation unit.
//
// It goes through the same flag builders the Ninja rules use, so that what a
// language server is told matches what the compiler is given. Two renderings
// that drifted would produce an editor confidently reporting errors in code
// that builds.
func (c *CompileCommands) commandFor(n *Ninja, compiler toolchain.Compiler, r *Resolved, source, object string) string {
	parts := []string{quoteCommand(compiler.Path)}
	if c.Toolchain.Kind() == toolchain.MSVC {
		parts = append(parts, "/nologo")
	}
	if defines := n.defineFlags(r.Defines); defines != "" {
		parts = append(parts, defines)
	}
	if includes := n.includeFlags(r.IncludeDirs); includes != "" {
		parts = append(parts, includes)
	}
	if flags := strings.Join(r.CompileOpts, " "); flags != "" {
		parts = append(parts, flags)
	}
	if c.Toolchain.Kind() == toolchain.MSVC {
		parts = append(parts, "/c", quoteArg(source), "/Fo"+quoteArg(object))
	} else {
		parts = append(parts, "-c", quoteArg(source), "-o", quoteArg(object))
	}
	return strings.Join(parts, " ")
}

// Write emits the database as JSON. It is not called WriteTo: that name belongs
// to io.WriterTo, whose contract is to report the byte count.
func (c *CompileCommands) Write(w io.Writer) error {
	entries := c.Entries()
	if entries == nil {
		// An empty array rather than null: a consumer reading null has to guess
		// whether the project has no sources or the file is broken.
		entries = []CompileCommand{}
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err = fmt.Fprintln(w)
	return err
}
