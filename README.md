# valfile

A CLI tool to statically validate YAML, TOML, JSON and Jsonnet files against a Go type.

The following command will return errors, if file `input-file.toml`
can't be unmarshaled into type `YourType` defined in package `path/to/yourpackage`
or doesn't match it's `validate` field tags of
[github.com/go-playground/validator](https://github.com/go-playground/validator):


```sh
valfile -p path/to/yourpackage -t YourType -f input-file.toml
```

If `input-file.toml` passes the marshaling and validation then
the above command is a no-op.

## Requirements

`valfile` requires the Go compiler toolchain to be installed on the system.

## How it works

`valfile` parses the given package, finds the type definition, renders a format-specific
program template to a temporary directory, runs it using the Go toolchain `go run .`
and forwards error messages if any.
