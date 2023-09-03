# valfile

A CLI tool to statically validate YAML, TOML, JSON, Jsonnet, dotenv files and
environment variables against a Go `struct` type.

## Usage

The following command will return errors, if file `input-file.toml`
can't be unmarshaled into type `YourStructType` defined in package `path/to/yourpackage`
or doesn't match it's `validate` field tags of
[github.com/go-playground/validator](https://github.com/go-playground/validator):


```sh
valfile -p path/to/yourpackage -t YourStructType -f input-file.toml
```

If `input-file.toml` passes the marshaling and validation then
the above command is a no-op.

### Environment variables

To match environment variables against a Go type, use the `-env` flag.
Beware that this flag is mutually exclusive with `-f`.

```sh
FOO=bar BAZZ=fuzz valfile -p path/to/yourpackage -t YourStructType -env
```

## Requirements

`valfile` requires the Go compiler toolchain to be installed on the system.

## How it works

`valfile` parses the given package, finds the type definition, renders a format-specific
program template to a temporary directory, runs it using the Go toolchain `go run .`
and forwards error messages if any.
