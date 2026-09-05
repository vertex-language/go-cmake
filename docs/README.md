# docs

[`grammar.md`](grammar.md) is this project's own: the CMake language written as
productions, because the CMake documentation describes behaviour but publishes
no grammar a parser can be written against.

Everything else in this directory is a verbatim dump of CMake's own reference
documentation, kept locally while implementing so that a command's exact wording
is one grep away. It is Kitware's text under CMake's BSD-3-Clause licence, not
this project's, so it is not committed here. Regenerate it with:

```sh
cmake --help-command-list          > docs/cmake-commands.txt
cmake --help-commands              > docs/cmake-commands-full.txt
cmake --help-variable-list         > docs/cmake-variables.txt
cmake --help-variables             > docs/cmake-variables-full.txt
cmake --help-property-list         > docs/cmake-properties.txt
cmake --help-properties            > docs/cmake-properties-full.txt
cmake --help-manual cmake-language > docs/cmake-language.txt
cmake --help-manual cmake-generator-expressions > docs/cmake-genex.txt
cmake --help-manual cmake-presets  > docs/cmake-presets.txt
```

These files are reference material, not a specification this implementation is
tested against. The specification is the `cmake` binary itself: see the
differential tests in [`../eval`](../eval), which run every construct through
both implementations and require the output to match.
