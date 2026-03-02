package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// A thin wrapper over go.uber.org/mock/mockgen with sensible defaults.
// Usage in code, if global install:
//
//	//go:generate toolsmocks
//
// Usage in code, if local project install:
//
//	//go:generate toolsmocks
//
// Or with overrides:
//
//	//go:generate mockgen -source=$GOFILE -destination=mocks/usecase_mock.gen.go -package=${GOPACKAGE}mocks
func main() {
	gofile := os.Getenv("GOFILE")
	gopackage := os.Getenv("GOPACKAGE")

	var (
		src  string
		out  string
		pkg  string
		args string
	)
	flag.StringVar(&src, "source", "", "source file to scan (default: $GOFILE)")
	flag.StringVar(&out, "destination", "", "output file (default: mocks/<base>_mock.gen.go)")
	flag.StringVar(&pkg, "package", "", "package name for mocks (default: ${GOPACKAGE}mocks)")
	flag.StringVar(&args, "args", "", "extra args to pass to mockgen")
	flag.Parse()

	if src == "" {
		src = gofile
	}
	if src == "" {
		log.Fatal("source not set and GOFILE is empty; run via go:generate or pass -source")
	}

	if pkg == "" {
		if gopackage == "" {
			log.Fatal("GOPACKAGE not set; pass -package explicitly")
		}
		pkg = gopackage + "mocks"
	}

	if out == "" {
		base := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		out = filepath.Join("mocks", base+"_mock.gen.go")
	}

	cmdArgs := []string{
		"-source=" + src,
		"-destination=" + out,
		"-package=" + pkg,
	}

	if args != "" {
		extra := strings.Fields(strings.ReplaceAll(args, ",", " "))
		cmdArgs = append(cmdArgs, extra...)
	}

	//go:generate mockgen -source=$GOFILE -destination=mocks/usecase_mock.gen.go -package=${GOPACKAGE}mocks
	//nolint:gosec // internal tool for code generation so args are trusted
	log.Println("//go:generate mockgen", strings.Join(cmdArgs, " "))

	ctx := context.Background()
	//nolint:gosec // internal tool for code generation so args are trusted
	cmd := exec.CommandContext(ctx, "mockgen", cmdArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		log.Fatal(err)
	}
}
