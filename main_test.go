package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLI(t *testing.T) {
	for _, td := range []Test{
		// CLI Parameters
		{
			Name: "err_missing_type",
			Args: "-p $SETUP/tstcmd -env",
			Files: map[string]string{
				"tstcmd/main.go": `
					package main
					type Config struct { Foo "json:\"bar\"" }
				`,
			},
			ExpectErrs: []string{"missing type name"},
		},

		// Success
		{
			Name:    "env_vars",
			Args:    "-p $SETUP/tstcmd -t Config -env",
			EnvVars: []string{"FOO=bar"},
			Files: map[string]string{
				"tstcmd/main.go": `
					package main
					type Config struct { Foo string "env:\"foo\"" }
				`,
			},
		},
		{
			Name: "json",
			Args: "-p $SETUP/tstcmd -t Config -f $SETUP/input.json",
			Files: map[string]string{
				"input.json": `{"foo":"bar"}`,
				"tstcmd/main.go": `
					package main
					type Config struct { Foo string "json:\"foo\"" }
				`,
			},
		},
		{
			Name: "toml",
			Args: "-p $SETUP/tstcmd -t Config -f $SETUP/input.toml",
			Files: map[string]string{
				"input.toml": `foo = "bar"`,
				"tstcmd/main.go": `
					package main
					type Config struct { Foo string "toml:\"foo\"" }
				`,
			},
		},

		// Format: JSON
		{
			Name: "err_unmarshaling_json_unknown_field",
			Args: "-p $SETUP/tstcmd -t Config -f $SETUP/input.json",
			Files: map[string]string{
				"input.json": `{"bar":"baz"}`,
				"tstcmd/main.go": `
					package main
					type Config struct { Foo string "json:\"baz\"" }
				`,
			},
			ExpectErrs: []string{`json: unknown field "bar"`},
		},

		// Format: TOML

	} {
		t.Run(td.Name, func(t *testing.T) {
			td.validateName(t)

			dir := prepareTestSetup(t, td)

			// Replace variable $SETUP with the actual setup directory path
			td.Args = strings.ReplaceAll(td.Args, "$SETUP", dir)

			// Include the executable name as first argument
			args := append([]string{"valfile"}, strings.Fields(td.Args)...)

			errs := run(args, t.TempDir, func() []string { return td.EnvVars })
			if td.ExpectErrs == nil {
				require.Nil(t, errs, "unexpected errors: %v", errs)
				return
			}
			require.Equal(t, td.ExpectErrs, toStrings(errs))
		})
	}
}

type Test struct {
	Name       string
	Args       string            // CLI arguments without the first executable name
	Files      map[string]string // file name to contents mapping
	ExpectErrs []string          // expected error messages
	EnvVars    []string          // key-value pairs
}

func (td Test) validateName(t *testing.T) {
	if strings.HasPrefix(td.Name, "err_") {
		if len(td.ExpectErrs) < 1 {
			t.Fatalf("error test expects no errors")
		}
	} else if len(td.ExpectErrs) > 0 {
		t.Fatalf("success test expects %d error(s)", len(td.ExpectErrs))
	}
}

func toStrings[T any](s []T) []string {
	msgs := make([]string, len(s))
	for i := range s {
		msgs[i] = fmt.Sprintf("%v", s[i])
	}
	return msgs
}

// prepareTestSetup creates all test files in a temporary directory
// and returns its path.
func prepareTestSetup(t *testing.T, td Test) (setupDir string) {
	setupDir = t.TempDir()
	for path, contents := range td.Files {
		path := filepath.Join(setupDir, path)
		err := os.MkdirAll(filepath.Dir(path), 0o777)
		require.NoError(t, err)
		err = os.WriteFile(path, []byte(contents), 0o777)
		require.NoError(t, err)
	}
	return setupDir
}
