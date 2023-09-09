package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"github.com/fatih/structtag"
	"github.com/google/go-jsonnet"
	"github.com/joho/godotenv"
)

//go:embed tmpl_main_env.go.tmpl
var tmplMainENV string

//go:embed tmpl_main_toml.go.tmpl
var tmplMainTOML string

//go:embed tmpl_main_json.go.tmpl
var tmplMainJSON string

//go:embed tmpl_main_yaml.go.tmpl
var tmplMainYAML string

//go:embed tmpl_main_hcl.go.tmpl
var tmplMainHCL string

//go:embed tmpl_validate.go.tmpl
var tmplSrcValidate string

//go:embed vendor_env.zip
var vendorENV []byte

//go:embed vendor_toml.zip
var vendorTOML []byte

//go:embed vendor_json.zip
var vendorJSON []byte

//go:embed vendor_yaml.zip
var vendorYAML []byte

//go:embed vendor_hcl.zip
var vendorHCL []byte

//go:embed tmpl_gomod_env.txt
var gomodENV []byte

//go:embed tmpl_gomod_toml.txt
var gomodTOML []byte

//go:embed tmpl_gomod_json.txt
var gomodJSON []byte

//go:embed tmpl_gomod_yaml.txt
var gomodYAML []byte

//go:embed tmpl_gomod_hcl.txt
var gomodHCL []byte

//go:embed tmpl_gosum_env.txt
var gosumENV []byte

//go:embed tmpl_gosum_toml.txt
var gosumTOML []byte

//go:embed tmpl_gosum_json.txt
var gosumJSON []byte

//go:embed tmpl_gosum_yaml.txt
var gosumYAML []byte

//go:embed tmpl_gosum_hcl.txt
var gosumHCL []byte

var (
	tmplValidate = template.Must(template.New("validate").Parse(tmplSrcValidate))
	tmplTOML     = withTmpl("main_toml", tmplMainTOML, tmplValidate)
	tmplJSON     = withTmpl("main_json", tmplMainJSON, tmplValidate)
	tmplYAML     = withTmpl("main_yaml", tmplMainYAML, tmplValidate)
	tmplHCL      = withTmpl("main_hcl", tmplMainHCL, tmplValidate)
	tmplENV      = withTmpl("main_env", tmplMainENV, tmplValidate)
)

func withTmpl(name, src string, t ...*template.Template) *template.Template {
	tmpl := template.Must(template.New(name).Parse(src))
	for _, t := range t {
		if _, err := tmpl.New(t.Name()).Parse(tmplSrcValidate); err != nil {
			panic(err)
		}
	}
	return tmpl
}

const StdoutErrPrefix = "VALFILE: "

func main() {
	if errs := run(os.Args, os.TempDir, os.Environ); len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stdout, err.Error())
		}
		os.Exit(1)
	}
}

func run(
	args []string,
	makeTmpDir func() string,
	envVars func() []string,
) (errs []error) {
	p, err := parseCLIParameters(args)
	if err != nil {
		return []error{err}
	}

	inputType := InputTypeENV
	if !p.InputEnv {
		var err error
		inputType, err = getFileFormat(p.InputFile)
		if err != nil {
			return []error{err}
		}
	}

	fset := token.NewFileSet()

	pkg, err := parsePackage(fset, p.PackageDir)
	if err != nil {
		return []error{err}
	}

	rootType := findType(fset, pkg, p.TypeName)
	if rootType == nil {
		return []error{
			fmt.Errorf("type %s not found in package %s\n", p.TypeName, pkg.Name),
		}
	}

	typeStr, err := renderGoType(rootType, fset)
	if err != nil {
		return []error{fmt.Errorf("rendering go type: %w", err)}
	}
	typeDefinitions := []string{typeStr}
	typeSpecs := map[string]*ast.TypeSpec{
		p.TypeName: rootType,
	}

	traverseTypeIdents(fset, pkg, rootType.Type, func(i *ast.Ident) bool {
		if isTypePrimitive(i.Name) {
			return false
		}
		t := findType(fset, pkg, i.Name)
		if t == nil {
			errs = append(errs, fmt.Errorf("undefined type: %s", i.Name))
			return true
		}
		if _, ok := typeSpecs[t.Name.Name]; ok {
			return false
		}
		r, err := renderGoType(t, fset)
		if err != nil {
			errs = append(errs, fmt.Errorf("rendering go type: %w", err))
			return true
		}
		typeSpecs[t.Name.Name] = t
		typeDefinitions = append(typeDefinitions, r)
		return false
	})
	if errs != nil {
		return errs
	}

	fileName := filepath.Base(p.InputFile)

	// Write format-specific executable to temporary file
	var source, goMod, goSum, vendorArchive []byte
	var expectMarshalingTag string
	switch inputType {
	case InputTypeENV:
		m := envToMap(envVars())
		source = mustRenderSrcEnv(typeDefinitions, p.TypeName, m)
		goMod, goSum, vendorArchive = gomodENV, gosumENV, vendorENV
		expectMarshalingTag = "env"
	case InputTypeDOTENV:
		f, err := os.OpenFile(p.InputFile, os.O_RDONLY, 0o644)
		if err != nil {
			return []error{fmt.Errorf("reading input file: %w", err)}
		}
		m, err := godotenv.Parse(f)
		if err != nil {
			return []error{fmt.Errorf("parsing dotenv file: %w", err)}
		}
		source = mustRenderSrcEnv(typeDefinitions, p.TypeName, m)
		goMod, goSum, vendorArchive = gomodENV, gosumENV, vendorENV
		expectMarshalingTag = "env"
	case InputTypeTOML:
		inputFileContents, err := os.ReadFile(p.InputFile)
		if err != nil {
			return []error{fmt.Errorf("reading input file: %w", err)}
		}
		source = mustRenderSrc(
			typeDefinitions, p.TypeName, string(inputFileContents), fileName, tmplTOML,
		)
		goMod, goSum, vendorArchive = gomodTOML, gosumTOML, vendorTOML
		expectMarshalingTag = "toml"
	case InputTypeJSON:
		inputFileContents, err := os.ReadFile(p.InputFile)
		if err != nil {
			return []error{fmt.Errorf("reading input file: %w", err)}
		}
		source = mustRenderSrc(
			typeDefinitions, p.TypeName, string(inputFileContents), fileName, tmplJSON,
		)
		goMod, goSum, vendorArchive = gomodJSON, gosumJSON, vendorJSON
		expectMarshalingTag = "json"
	case InputTypeYAML:
		inputFileContents, err := os.ReadFile(p.InputFile)
		if err != nil {
			return []error{fmt.Errorf("reading input file: %w", err)}
		}
		source = mustRenderSrc(
			typeDefinitions, p.TypeName, string(inputFileContents), fileName, tmplYAML,
		)
		goMod, goSum, vendorArchive = gomodYAML, gosumYAML, vendorYAML
		expectMarshalingTag = "yaml"
	case InputTypeJSONNET:
		vm := jsonnet.MakeVM()
		rendered, err := vm.EvaluateFile(p.InputFile)
		if err != nil {
			return []error{fmt.Errorf("evaluating Jsonnet: %w", err)}
		}
		source = mustRenderSrc(
			typeDefinitions, p.TypeName, rendered, fileName, tmplJSON,
		)
		goMod, goSum, vendorArchive = gomodJSON, gosumJSON, vendorJSON
		expectMarshalingTag = "json"
	case InputTypeHCL:
		inputFileContents, err := os.ReadFile(p.InputFile)
		if err != nil {
			return []error{fmt.Errorf("reading input file: %w", err)}
		}
		source = mustRenderSrc(
			typeDefinitions, p.TypeName, string(inputFileContents), fileName, tmplHCL,
		)
		goMod, goSum, vendorArchive = gomodHCL, gosumHCL, vendorHCL
		expectMarshalingTag = "hcl"
	}

	if !p.NoTagCheck {
		for _, k := range sortedKeys(typeSpecs) {
			t := typeSpecs[k]
			if err := checkMarshalingTags(t, expectMarshalingTag); len(err) > 0 {
				errs = append(errs, err...)
			}
		}
		if errs != nil {
			return errs
		}
	}

	tempDir, err := os.MkdirTemp(makeTmpDir(), "valfile-*")
	if err != nil {
		return []error{fmt.Errorf("creating temporary directory: %w", err)}
	}
	defer os.RemoveAll(tempDir)

	{
		p := filepath.Join(tempDir, "main.go")
		if err = os.WriteFile(p, source, 0o644); err != nil {
			return []error{fmt.Errorf("writing %s: %w", p, err)}
		}
	}
	{
		p := filepath.Join(tempDir, "go.mod")
		if err = os.WriteFile(p, goMod, 0o644); err != nil {
			return []error{fmt.Errorf("writing %s: %w", p, err)}
		}
	}
	{
		p := filepath.Join(tempDir, "go.sum")
		if err = os.WriteFile(p, goSum, 0o644); err != nil {
			return []error{fmt.Errorf("writing %s: %w", p, err)}
		}
	}

	if err = unzipArchive(vendorArchive, tempDir); err != nil {
		return []error{fmt.Errorf("unzipping vendor directory: %w", err)}
	}

	// Compile and run the executable
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = tempDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return []error{err}
	}
	output = bytes.TrimRight(output, "\n")

	if bytes.HasPrefix(output, []byte(StdoutErrPrefix)) {
		msg := output[len(StdoutErrPrefix):]
		return []error{errors.New(string(msg))}
	}
	return nil
}

type Params struct {
	PackageDir string
	TypeName   string
	InputFile  string
	InputEnv   bool
	NoTagCheck bool
}

func parseCLIParameters(args []string) (Params, error) {
	var params Params
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	f.StringVar(&params.PackageDir, "p", ".", "package directory path")
	f.StringVar(&params.TypeName, "t", "", "type name")
	f.StringVar(&params.InputFile, "f", "", "path to input file")
	f.BoolVar(&params.InputEnv, "env", false, "use environment variables as input")
	f.BoolVar(
		&params.NoTagCheck,
		"no-tag-check", false, "disables check of marshaling tags if set",
	)
	if err := f.Parse(args[1:]); err != nil {
		return Params{}, err
	}

	switch {
	case params.PackageDir == "":
		return Params{}, errors.New("missing package directory")
	case params.TypeName == "":
		return Params{}, errors.New("missing type name")
	case !params.InputEnv && params.InputFile == "":
		return Params{}, errors.New("missing input file")
	case params.InputEnv && params.InputFile != "":
		return Params{}, errors.New("conflicting parameters, " +
			"-env and -f are mutually exlusive. " +
			"Please use either the -env option or the -f option, but not both.")
	}

	return params, nil
}

func mustRenderSrc(
	typeDefinitions []string,
	rootTypeName, input, fileName string,
	tmpl *template.Template,
) []byte {
	b := new(bytes.Buffer)
	if err := tmpl.Execute(b, struct {
		TypeDefinitions []string
		RootTypeName    string
		Input           string
		InputFileName   string
		StdoutErrPrefix string
	}{
		TypeDefinitions: typeDefinitions,
		RootTypeName:    rootTypeName,
		Input:           input,
		InputFileName:   fileName,
		StdoutErrPrefix: StdoutErrPrefix,
	}); err != nil {
		panic(fmt.Errorf("executing template: %w", err))
	}
	return b.Bytes()
}

func mustRenderSrcEnv(
	typeDefinitions []string,
	rootTypeName string,
	input map[string]string,
) []byte {
	b := new(bytes.Buffer)
	if err := tmplENV.Execute(b, struct {
		TypeDefinitions []string
		RootTypeName    string
		Input           map[string]string
		StdoutErrPrefix string
	}{
		TypeDefinitions: typeDefinitions,
		RootTypeName:    rootTypeName,
		Input:           input,
		StdoutErrPrefix: StdoutErrPrefix,
	}); err != nil {
		panic(fmt.Errorf("executing template: %w", err))
	}
	return b.Bytes()
}

func parsePackage(fset *token.FileSet, packageDirPath string) (*ast.Package, error) {
	pkgs, err := parser.ParseDir(fset, packageDirPath, nil, parser.AllErrors)
	if err != nil {
		return nil, fmt.Errorf("parsing package: %s", err.Error())
	}
	if len(pkgs) != 1 {
		panic(fmt.Errorf("expected 1 package, received: %d", len(pkgs)))
	}
	for k := range pkgs {
		return pkgs[k], nil
	}
	return nil, nil
}

func findType(
	fset *token.FileSet,
	pkg *ast.Package,
	typeName string,
) *ast.TypeSpec {
	for _, file := range pkg.Files {
		for _, obj := range file.Scope.Objects {
			if obj.Kind != ast.Typ {
				continue
			}
			if obj.Name != typeName {
				continue
			}
			return obj.Decl.(*ast.TypeSpec)
		}
	}
	return nil
}

func checkMarshalingTags(t *ast.TypeSpec, expectTag string) (errs []error) {
	s, ok := t.Type.(*ast.StructType)
	if !ok {
		return nil
	}

	for _, f := range s.Fields.List {
		var fieldName string
		if len(f.Names) > 0 {
			fieldName = f.Names[0].Name
		} else if id, ok := f.Type.(*ast.Ident); ok {
			fieldName = id.Name
		}
		addErrf := func(msg string, v ...any) {
			errs = append(errs, fmt.Errorf(
				"%s.%s: %s", t.Name.Name, fieldName, fmt.Sprintf(msg, v...),
			))
		}
		if f.Tag == nil || f.Tag.Value == "" {
			addErrf("missing tag %q", expectTag)
			continue
		}

		tagContent, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			addErrf("unquoting tag: %v", err)
		}

		tags, err := structtag.Parse(tagContent)
		if err != nil {
			addErrf("parsing struct tags: %v", err)
			continue
		}
		tag, err := tags.Get(expectTag)
		if err != nil {
			if err.Error() == "tag does not exist" {
				addErrf("missing tag %q", expectTag)
				continue
			}
			addErrf("getting tag %q: %v", expectTag, err)
			continue
		}
		if tag.Name == "" {
			addErrf("tag %q is empty", expectTag)
			continue
		}
	}
	return errs
}

func traverseTypeIdents(
	fset *token.FileSet,
	pkg *ast.Package,
	e ast.Expr,
	fn func(*ast.Ident) (stop bool),
) {
	switch t := e.(type) {
	case *ast.ChanType, *ast.FuncType:
	case *ast.StructType:
		for _, f := range t.Fields.List {
			traverseTypeIdents(fset, pkg, f.Type, fn)
		}
	case *ast.ArrayType:
		traverseTypeIdents(fset, pkg, t.Elt, fn)
	case *ast.MapType:
		traverseTypeIdents(fset, pkg, t.Key, fn)
		traverseTypeIdents(fset, pkg, t.Value, fn)
	case *ast.Ident:
		id := e.(*ast.Ident)
		if fn(id) {
			return
		}
		if x := findType(fset, pkg, id.Name); x != nil {
			traverseTypeIdents(fset, pkg, x.Type, fn)
		}
	}
}

func isTypePrimitive(typeName string) bool {
	switch typeName {
	case "string", "bool", "byte", "rune", "uintptr",
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64", "complex64", "complex128":
		return true
	}
	return false
}

// renderGoType converts an *ast.TypeSpec to Go code text.
func renderGoType(node any, fileSet *token.FileSet) (string, error) {
	var buf bytes.Buffer
	err := format.Node(&buf, fileSet, node)
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

// unzipArchive unzips archive into directory dst.
func unzipArchive(archive []byte, dst string) error {
	// Create a new zip reader from the src
	zipReader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("creating zip reader: %w", err)
	}

	// Loop through each file in the zip archive
	for _, zipFile := range zipReader.File {
		if strings.HasSuffix(zipFile.Name, "/") {
			continue
		}

		// Generate the full path for the destination file
		destPath := filepath.Join(dst, zipFile.Name)

		// Check for ZipSlip (Directory traversal)
		if !strings.HasPrefix(destPath, filepath.Clean(dst)+string(os.PathSeparator)) {
			return fmt.Errorf("illegal file path: %s", destPath)
		}

		// Create necessary enclosing directories for the file
		if err = os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("creating directory: %w", err)
		}

		// Create or overwrite the file at the destination path
		fileWriter, err := os.Create(destPath)
		if err != nil {
			return fmt.Errorf("creating file: %w", err)
		}

		// Open the file in the archive
		fileReader, err := zipFile.Open()
		if err != nil {
			return fmt.Errorf("opening file in archive: %w", err)
		}

		// Copy the contents of the file in the archive to the new file
		if _, err := io.Copy(fileWriter, fileReader); err != nil {
			return fmt.Errorf("copying file contents: %w", err)
		}

		// Close the file and its reader
		_ = fileWriter.Close()
		_ = fileReader.Close()
	}

	return nil
}

func envToMap(envVars []string) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, v := range envVars {
		p := strings.SplitN(v, "=", 2)
		if len(p) != 2 {
			panic(fmt.Errorf("unexpected env var: %q", v))
		}
		m[p[0]] = p[1]
	}
	return m
}

type InputType int8

const (
	_ InputType = iota
	InputTypeTOML
	InputTypeJSON
	InputTypeJSONNET
	InputTypeYAML
	InputTypeENV
	InputTypeDOTENV
	InputTypeHCL
)

func getFileFormat(filePath string) (InputType, error) {
	extension := strings.ToLower(filepath.Ext(filePath))
	switch extension {
	case ".toml":
		return InputTypeTOML, nil
	case ".json":
		return InputTypeJSON, nil
	case ".jsonnet":
		return InputTypeJSONNET, nil
	case ".yaml", ".yml":
		return InputTypeYAML, nil
	case ".hcl":
		return InputTypeHCL, nil
	}
	fileName := filepath.Base(filePath)
	if regexEnvFile.MatchString(fileName) {
		return InputTypeDOTENV, nil
	}
	return 0, fmt.Errorf("unsupported file type: %q\n", fileName)
}

var regexEnvFile = regexp.MustCompile(`^\.env(\..+)?$`)

func sortedKeys[K comparable, V any](m map[K]V) []K {
	s := make([]K, 0, len(m))
	for k := range m {
		s = append(s, k)
	}
	return s
}
