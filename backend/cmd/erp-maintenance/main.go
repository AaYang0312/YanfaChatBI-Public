package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/google/uuid"
	"yanfachatbi/pkg/erpmaintenance"
)

func execute(args []string, out, errout io.Writer) int {
	flags := flag.NewFlagSet("erp-maintenance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source-id", "", "")
	if flags.Parse(args) != nil || flags.NArg() != 1 || flags.Arg(0) != "incremental" {
		fmt.Fprintln(errout, "INVALID_REQUEST")
		return 2
	}
	id, err := uuid.Parse(*source)
	if err != nil || id == uuid.Nil || id.String() != *source {
		fmt.Fprintln(errout, "INVALID_REQUEST")
		return 2
	}
	request := erpmaintenance.Request{Protocol: erpmaintenance.Protocol, SourceID: *source, RequestID: uuid.NewString(), Operation: "incremental"}
	uid := uint32(os.Geteuid())
	directory := fmt.Sprintf("/run/user/%d/yanfa-bi-chat/source-gates", uid)
	response, err := erpmaintenance.Call(directory, uid, request)
	if err != nil {
		json.NewEncoder(out).Encode(map[string]string{"request_id": request.RequestID, "code": err.Error()})
		return 1
	}
	json.NewEncoder(out).Encode(response)
	if response.Code != "ACCEPTED" {
		return 1
	}
	return 0
}
func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }
