package mongodb

import (
	"context"
	"fmt"
	"log"
	"reflect"
	"time"

	"github.com/shopspring/decimal"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/bsoncodec"
	"go.mongodb.org/mongo-driver/bson/bsonrw"
	"go.mongodb.org/mongo-driver/bson/bsontype"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoDBClient struct {
	Client *mongo.Client
	Config MongoDbConfigModel
}

var applicationRegistry = newApplicationRegistry()

// BSONRegistry is shared by the default application client and repository
// adapters. Treat it as immutable after construction. BSON strings preserve
// decimal.Decimal's arbitrary precision without float or Decimal128 rounding.
func BSONRegistry() *bsoncodec.Registry {
	return applicationRegistry
}

func newApplicationRegistry() *bsoncodec.Registry {
	registry := bson.NewRegistry()
	decimalType := reflect.TypeOf(decimal.Decimal{})
	registry.RegisterTypeEncoder(decimalType, bsoncodec.ValueEncoderFunc(encodeDecimal))
	registry.RegisterTypeDecoder(decimalType, bsoncodec.ValueDecoderFunc(decodeDecimal))
	return registry
}

func encodeDecimal(_ bsoncodec.EncodeContext, writer bsonrw.ValueWriter, value reflect.Value) error {
	return writer.WriteString(value.Interface().(decimal.Decimal).String())
}

func decodeDecimal(_ bsoncodec.DecodeContext, reader bsonrw.ValueReader, value reflect.Value) error {
	if reader.Type() != bsontype.String {
		return fmt.Errorf("decimal BSON type %v, want string", reader.Type())
	}
	text, err := reader.ReadString()
	if err != nil {
		return err
	}
	parsed, err := decimal.NewFromString(text)
	if err != nil {
		return err
	}
	value.Set(reflect.ValueOf(parsed))
	return nil
}

func InitializeDatabaseConnection(config MongoDbConfigModel) *MongoDBClient {
	// Replace with your MongoDB Atlas connection string
	connectionURI := config.ConnectionUrl

	clientOptions := options.Client().ApplyURI(connectionURI).SetRegistry(BSONRegistry())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var err error
	mongoClient, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		log.Fatalf("MongoDB Connection Error: %v", err)
	}

	// Ping the database to verify connection
	err = mongoClient.Ping(ctx, nil)
	if err != nil {
		log.Fatalf("MongoDB Ping Error: %v", err)
	}

	log.Println("✨ Connected to MongoDB.")

	return &MongoDBClient{
		Client: mongoClient,
		Config: config,
	}
}

func (client *MongoDBClient) GetCollectionByName(collectionName string) *mongo.Collection {
	collection := client.Client.Database(client.Config.DatabaseName).Collection(collectionName)
	return collection
}
