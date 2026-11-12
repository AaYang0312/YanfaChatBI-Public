package models

import (
	"go.mongodb.org/mongo-driver/bson/primitive"
	"yanfachatbi/pkg/schemaobjects"
)

type ChatSettings struct {
	AutoExecuteQuery          bool   `bson:"auto_execute_query" json:"auto_execute_query,omitempty"`                   // default is true, Execute query automatically when LLM response is received
	ShareDataWithAI           bool   `bson:"-" json:"-"`                                                               // Deprecated; retained for existing Go callers only.
	NonTechMode               bool   `bson:"non_tech_mode" json:"non_tech_mode,omitempty"`                             // default is false, Enable non-technical mode for simplified responses
	SelectedLLMModel          string `bson:"selected_llm_model" json:"selected_llm_model,omitempty"`                   // LLM model selected for this chat (e.g., "gpt-4o", "gemini-2.0-flash")
	AutoGenerateVisualization bool   `bson:"auto_generate_visualization" json:"auto_generate_visualization,omitempty"` // default is false, Auto-generate chart visualizations for compatible queries
	AutoAnalyzeResults        bool   `bson:"auto_analyze_results,omitempty" json:"auto_analyze_results,omitempty"`     // default is false, Ask the model to interpret executed results
}

type Connection struct {
	Type         string  `bson:"type" json:"type"`
	Host         string  `bson:"host" json:"host"`
	Port         *string `bson:"port" json:"port"`
	Username     *string `bson:"username" json:"username"`
	Password     *string `bson:"password" json:"-"` // Hide in JSON
	Database     string  `bson:"database" json:"database"`
	AuthDatabase *string `bson:"auth_database" json:"auth_database"` // Database to authenticate against
	IsExampleDB  bool    `bson:"is_example_db" json:"is_example_db"` // default is false, if true, then the database is an example database configs setup from environment variables

	// SSL/TLS Configuration
	UseSSL         bool    `bson:"use_ssl" json:"use_ssl"`
	SSLMode        *string `bson:"ssl_mode,omitempty" json:"ssl_mode,omitempty"` // type: disable, require, verify-ca, verify-full
	SSLCertURL     *string `bson:"ssl_cert_url,omitempty" json:"ssl_cert_url,omitempty"`
	SSLKeyURL      *string `bson:"ssl_key_url,omitempty" json:"ssl_key_url,omitempty"`
	SSLRootCertURL *string `bson:"ssl_root_cert_url,omitempty" json:"ssl_root_cert_url,omitempty"`

	// SSH Tunnel Configuration
	SSHEnabled       bool    `bson:"ssh_enabled,omitempty" json:"ssh_enabled,omitempty"`
	SSHHost          *string `bson:"ssh_host,omitempty" json:"ssh_host,omitempty"`
	SSHPort          *string `bson:"ssh_port,omitempty" json:"ssh_port,omitempty"`
	SSHUsername      *string `bson:"ssh_username,omitempty" json:"ssh_username,omitempty"`
	SSHAuthMethod    *string `bson:"ssh_auth_method,omitempty" json:"ssh_auth_method,omitempty"`         // "publickey" or "password"
	SSHPrivateKey    *string `bson:"ssh_private_key,omitempty" json:"-"`                                 // Hide in JSON
	SSHPrivateKeyURL *string `bson:"ssh_private_key_url,omitempty" json:"ssh_private_key_url,omitempty"` // URL to fetch from
	SSHPassphrase    *string `bson:"ssh_passphrase,omitempty" json:"-"`                                  // Hide in JSON
	SSHPassword      *string `bson:"ssh_password,omitempty" json:"-"`                                    // Hide in JSON

	// Google Sheets specific fields
	GoogleSheetID      *string `bson:"google_sheet_id,omitempty" json:"google_sheet_id,omitempty"`
	GoogleSheetURL     *string `bson:"google_sheet_url,omitempty" json:"google_sheet_url,omitempty"` // Encrypted, show in JSON for user reference
	GoogleAuthToken    *string `bson:"google_auth_token,omitempty" json:"-"`                         // Hide in JSON
	GoogleRefreshToken *string `bson:"google_refresh_token,omitempty" json:"-"`                      // Hide in JSON

	// Schema Cache - stores formatted schema for LLM context
	CurrentSchema   *string             `bson:"current_schema,omitempty" json:"current_schema,omitempty"`       // Formatted schema string ready for LLM
	SchemaUpdatedAt *primitive.DateTime `bson:"schema_updated_at,omitempty" json:"schema_updated_at,omitempty"` // When schema was last fetched/updated

	Base `bson:",inline"`
}

// Clone owns pointed-to fields: encryption/decryption mutates those values.
func (c Connection) Clone() Connection {
	copyString := func(value *string) *string {
		if value == nil {
			return nil
		}
		copied := *value
		return &copied
	}
	c.Port, c.Username, c.Password = copyString(c.Port), copyString(c.Username), copyString(c.Password)
	c.AuthDatabase, c.SSLMode = copyString(c.AuthDatabase), copyString(c.SSLMode)
	c.SSLCertURL, c.SSLKeyURL, c.SSLRootCertURL = copyString(c.SSLCertURL), copyString(c.SSLKeyURL), copyString(c.SSLRootCertURL)
	c.SSHHost, c.SSHPort, c.SSHUsername = copyString(c.SSHHost), copyString(c.SSHPort), copyString(c.SSHUsername)
	c.SSHAuthMethod, c.SSHPrivateKey, c.SSHPrivateKeyURL = copyString(c.SSHAuthMethod), copyString(c.SSHPrivateKey), copyString(c.SSHPrivateKeyURL)
	c.SSHPassphrase, c.SSHPassword = copyString(c.SSHPassphrase), copyString(c.SSHPassword)
	c.GoogleSheetID, c.GoogleSheetURL = copyString(c.GoogleSheetID), copyString(c.GoogleSheetURL)
	c.GoogleAuthToken, c.GoogleRefreshToken = copyString(c.GoogleAuthToken), copyString(c.GoogleRefreshToken)
	c.CurrentSchema = copyString(c.CurrentSchema)
	if c.SchemaUpdatedAt != nil {
		updated := *c.SchemaUpdatedAt
		c.SchemaUpdatedAt = &updated
	}
	return c
}

type Chat struct {
	SelectedObjects   *schemaobjects.Selection `bson:"selected_objects,omitempty" json:"selected_objects,omitempty"`
	SelectionRevision int64                    `bson:"selection_revision,omitempty" json:"selection_revision"`
	ObjectState       *State                   `bson:"object_state,omitempty" json:"-"`
	UserID            primitive.ObjectID       `bson:"user_id" json:"user_id"`
	DisplayName       string                   `bson:"display_name" json:"display_name,omitempty"`
	// Server-only source identity; request DTOs cannot set these fields.
	SourceID                    string       `bson:"source_id,omitempty" json:"source_id,omitempty"`
	SourceBindingRevision       int64        `bson:"source_binding_revision,omitempty" json:"source_binding_revision,omitempty"`
	SourceConnectionFingerprint string       `bson:"source_connection_fingerprint,omitempty" json:"-"`
	SourceBindingConfirmed      bool         `bson:"source_binding_confirmed" json:"-"`
	ERPAssociated               bool         `bson:"erp_associated" json:"-"`
	ConnectionRevision          int64        `bson:"connection_revision,omitempty" json:"-"`
	Connection                  Connection   `bson:"connection" json:"connection"`
	SelectedCollections         string       `bson:"selected_collections" json:"selected_collections"` // "ALL" or comma-separated table names
	Settings                    ChatSettings `bson:"settings" json:"settings"`
	PreferredLLMModel           *string      `bson:"preferred_llm_model" json:"preferred_llm_model"` // User's preferred LLM model for this chat
	PreferredReasoningLevel     string       `bson:"preferred_reasoning_level,omitempty" json:"preferred_reasoning_level,omitempty"`
	Base                        `bson:",inline"`
}

func NewChat(userID primitive.ObjectID, connection Connection, settings ChatSettings) *Chat {
	settings.ShareDataWithAI = false
	return &Chat{
		UserID:              userID,
		Connection:          connection,
		Settings:            settings,
		SelectedCollections: "ALL", // Default to ALL tables
		Base:                NewBase(),
	}
}

func DefaultChatSettings() ChatSettings {
	return ChatSettings{
		AutoExecuteQuery:          true,  // default is true, Execute query automatically when LLM response is received
		ShareDataWithAI:           false, // default is false, Don't share data with AI
		NonTechMode:               false, // default is false, Technical mode enabled by default
		AutoGenerateVisualization: false, // default is false, Don't auto-generate visualizations
		AutoAnalyzeResults:        false, // default is false, Don't analyze results automatically
	}
}
