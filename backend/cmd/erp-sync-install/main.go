package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"yanfachatbi/internal/erpsyncinstall"
)

func execute(args []string, out, errout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errout, "ERP_INSTALL_INPUT_INVALID")
		return 2
	}
	flags := flag.NewFlagSet("erp-sync-install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "")
	if flags.Parse(args[1:]) != nil || *input == "" || flags.NArg() != 0 {
		fmt.Fprintln(errout, "ERP_INSTALL_INPUT_INVALID")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var result interface{}
	var err error
	switch args[0] {
	case "prepare", "install":
		var req erpsyncinstall.Request
		err = erpsyncinstall.ReadJSON(*input, &req)
		if err == nil {
			if args[0] == "prepare" {
				result, err = erpsyncinstall.Prepare(ctx, req)
			} else {
				result, err = erpsyncinstall.Install(ctx, req)
			}
		}
	case "verify":
		var req erpsyncinstall.VerificationRequest
		err = erpsyncinstall.ReadJSON(*input, &req)
		if err == nil {
			err = erpsyncinstall.Verify(req)
		}
		result = map[string]string{"code": "VERIFIED"}
	case "convert-config":
		var req erpsyncinstall.ConvertRequest
		err = erpsyncinstall.ReadJSON(*input, &req)
		if err == nil {
			err = erpsyncinstall.ConvertConfig(req)
		}
		result = map[string]string{"code": "CONVERTED"}
	default:
		err = erpsyncinstall.ErrInput
	}
	if err != nil {
		fmt.Fprintln(errout, err.Error())
		return 1
	}
	if json.NewEncoder(out).Encode(result) != nil {
		return 1
	}
	return 0
}
func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }
