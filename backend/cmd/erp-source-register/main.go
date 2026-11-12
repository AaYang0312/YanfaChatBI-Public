package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"yanfachatbi/config"
	"yanfachatbi/internal/repositories"
	"yanfachatbi/internal/services"
	"yanfachatbi/pkg/mongodb"
	"yanfachatbi/pkg/sourcegate"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func execute(args []string, out, errout io.Writer) int {
	if len(args) == 0 || args[0] != "stage-view" && args[0] != "enable-sync" {
		fmt.Fprintln(errout, "REGISTRATION_INPUT_INVALID")
		return 2
	}
	flags := flag.NewFlagSet("erp-source-register", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	input := flags.String("input", "", "")
	directory := flags.String("output-dir", "", "")
	if flags.Parse(args[1:]) != nil || *input == "" || *directory == "" || flags.NArg() != 0 {
		fmt.Fprintln(errout, "REGISTRATION_INPUT_INVALID")
		return 2
	}
	var view services.ERPRegistrationViewRequest
	var enable services.ERPRegistrationEnableRequest
	var cfg config.ERPSyncConfig
	var err error
	if args[0] == "stage-view" {
		err = services.ReadERPRegistrationJSON(*input, &view)
		if err == nil {
			cfg, _, err = services.LoadERPRegistrationConfig(view.PublicConfigPath)
		}
	} else {
		err = services.ReadERPRegistrationJSON(*input, &enable)
		if err == nil {
			cfg, _, err = services.LoadERPRegistrationConfig(enable.PublicConfigPath)
		}
	}
	if err != nil {
		fmt.Fprintln(errout, "REGISTRATION_INPUT_INVALID")
		return 1
	}
	// Infrastructure and the existing encryption key are explicit operator inputs.
	// No dotenv loading, application startup, default database or new connection.
	uri, name, key := os.Getenv("YANFA_MONGODB_URI"), os.Getenv("YANFA_MONGODB_NAME"), os.Getenv("SCHEMA_ENCRYPTION_KEY")
	if uri == "" || name == "" || key == "" {
		fmt.Fprintln(errout, "REGISTRATION_INFRASTRUCTURE_MISSING")
		return 1
	}
	config.Env.SchemaEncryptionKey = key
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetServerSelectionTimeout(5*time.Second).SetRegistry(mongodb.BSONRegistry()))
	if err != nil {
		fmt.Fprintln(errout, "REGISTRATION_STATE_UNAVAILABLE")
		return 1
	}
	defer client.Disconnect(context.Background())
	if client.Ping(ctx, readpref.Primary()) != nil {
		fmt.Fprintln(errout, "REGISTRATION_STATE_UNAVAILABLE")
		return 1
	}
	gate, err := sourcegate.New(cfg.LockDirectory, cfg.DeploymentUID)
	if err != nil {
		fmt.Fprintln(errout, "REGISTRATION_GATE_UNAVAILABLE")
		return 1
	}
	defer gate.Close()
	wrapper := &mongodb.MongoDBClient{Client: client, Config: mongodb.MongoDbConfigModel{DatabaseName: name}}
	chats := repositories.NewChatRepository(wrapper, nil)
	access, err := services.NewSourceAccessService(repositories.NewERPSourceRepository(client.Database(name)), gate, cfg, chats)
	if err != nil {
		fmt.Fprintln(errout, "REGISTRATION_INPUT_INVALID")
		return 1
	}
	registrar, err := services.NewERPSourceRegistrationService(access, repositories.NewDataSyncRepository(client.Database(name)), cfg)
	if err != nil {
		fmt.Fprintln(errout, "REGISTRATION_INPUT_INVALID")
		return 1
	}
	var result *services.ERPRegistrationOutput
	if args[0] == "stage-view" {
		result, err = registrar.StageView(ctx, view)
	} else {
		result, err = registrar.EnableSync(ctx, enable)
	}
	if err != nil {
		fmt.Fprintln(errout, err.Error())
		return 1
	}
	if services.WriteERPRegistrationOutput(*directory, args[0], result) != nil {
		fmt.Fprintln(errout, "REGISTRATION_OUTPUT_CONFLICT")
		return 1
	}
	json.NewEncoder(out).Encode(map[string]string{"source_id": result.SourceID, "public_config_sha256": result.PublicConfigSHA256, "phase": result.Receipt.Phase})
	return 0
}
func main() { os.Exit(execute(os.Args[1:], os.Stdout, os.Stderr)) }
