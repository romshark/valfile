# valfile

A CLI tool to statically validate YAML, TOML, JSON, Jsonnet, HCL, dotenv files and
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

### Struct tag check

By default, valfile will return errors if any of the fields of the selected type
don't have a tag corresponding to the input type, for example:

```go
struct S struct {
    Foo string `json:"foo"`
    Bar string
}
```

The above type in combination with a JSON input file will produce:

```sh
Config.Bar: missing tag "json"
```

option `-no-tag-check` disables this check.

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
