package config

import (
	"fmt"
	"path/filepath"
)

// ERPSyncConfig is supplied only by a trusted deployment composition root.
// Zero configuration registers no sources and grants no permissions. It is not
// read from a Chat DTO, database display name, implicit localhost or driver type.
type ERPSyncConfig struct {
	InstanceID    string
	DeploymentUID uint32
	LockDirectory string
	Associations  []ERPAssociation
	Sync          ERPSyncRuntime
}

// ERPAssociation is the finite, approved identity directory. A candidate has no
// SourceID until registration is approved. Keep the directory available without
// Mongo: unrelated independent sources must not depend on ERP state availability.
type ERPAssociation struct {
	SourceID             string
	EndpointFingerprints []string
	ApprovalRef          string
}

// ValidateDeployment enforces the public production path. Isolated tests inject
// their own controlled directory directly into sourcegate.New, without enabling DI.
func (c ERPSyncConfig) ValidateDeployment() error {
	want := fmt.Sprintf("/run/user/%d/yanfa-bi-chat/source-gates", c.DeploymentUID)
	if c.InstanceID == "" || c.LockDirectory != want || filepath.Clean(c.LockDirectory) != want {
		return fmt.Errorf("ERP deployment identity or lock directory is unapproved")
	}
	for _, association := range c.Associations {
		if association.ApprovalRef == "" || len(association.EndpointFingerprints) == 0 {
			return fmt.Errorf("ERP association lacks approval evidence")
		}
	}
	return nil
}

// SecretInjection describes private injection metadata. It intentionally has no
// field for values, environment snapshots, raw argv or shell source text.
type SecretInjection struct {
	SecretRef    string   `bson:"secret_ref" json:"secret_ref"`
	ProviderKind string   `bson:"provider_kind" json:"provider_kind"`
	CustodianRef string   `bson:"custodian_ref" json:"custodian_ref"`
	VersionRef   string   `bson:"version_ref" json:"version_ref"`
	RequiredKeys []string `bson:"required_keys" json:"required_keys"`
	UID          uint32   `bson:"uid" json:"uid"`
	Mode         uint32   `bson:"mode" json:"mode"`
	RotationRef  string   `bson:"rotation_ref" json:"rotation_ref"`
}

// ERPSyncRuntime is public deployment metadata. Secret files are opened only by
// the runner/control reader; values never enter this document or a job.
type ERPSyncRuntime struct {
	Sources        map[string]ERPSourceRuntime
	StartSeconds   int
	RunSeconds     int
	ControlSeconds int
	TermSeconds    int
	KillSeconds    int
}

type ERPSourceRuntime struct {
	ManifestRef              string
	ManifestPath             string
	ManifestSHA256           string
	InstallationPath         string
	InstallationSHA256       string
	PrivateEnvironment       SecretFileReference
	ControlReader            SecretFileReference
	AftersaleSource          string
	RuntimeMountNamespace    string
	RuntimePIDNamespace      string
	ReaderRoleByRef          map[string]string
	WriterRole               string
	ControlRole              string
	ObservationApprovalRef   string
	SingleBackendApprovalRef string
	G15ApprovalRef           string
}

type SecretFileReference struct {
	Reference string
	Path      string
	UID       uint32
	Mode      uint32
}

// ERPComponentManifest describes the owner release, not the current worktree.
// ExecutableAbsolutePath is the sole executable path and always argv[0].
type ERPComponentManifest struct {
	ComponentOwnerRef      string   `json:"component_owner_ref"`
	RepositoryURI          string   `json:"repository_uri"`
	CommitOrRelease        string   `json:"commit_or_release"`
	ArtifactSHA256         string   `json:"artifact_sha256"`
	LockSHA256             string   `json:"lock_sha256"`
	BridgeProtocol         string   `json:"bridge_protocol"`
	BridgeVersion          string   `json:"bridge_version"`
	PythonVersion          string   `json:"python_version"`
	UVVersion              string   `json:"uv_version"`
	InstallRecipe          string   `json:"install_recipe"`
	ExecutableAbsolutePath string   `json:"executable_absolute_path"`
	WorkingDirectory       string   `json:"working_directory"`
	DeploymentUID          uint32   `json:"deployment_uid"`
	ConfigRef              string   `json:"config_ref"`
	ApprovedBy             string   `json:"approved_by"`
	ApprovedAt             string   `json:"approved_at"`
	MaintenanceOperations  []string `json:"maintenance_operations"`
}

// The operator attests the installed immutable tree against the owner artifact
// and lock. Runtime also hashes every listed installed file, including Python.
type ERPInstallationReceipt struct {
	ManifestSHA256        string            `json:"manifest_sha256"`
	ArtifactPath          string            `json:"artifact_path"`
	LockPath              string            `json:"lock_path"`
	ExecutableSHA256      string            `json:"executable_sha256"`
	InstalledFiles        map[string]string `json:"installed_files"`
	PythonVersion         string            `json:"python_version"`
	UVVersion             string            `json:"uv_version"`
	InstallRecipe         string            `json:"install_recipe"`
	RuntimeMountNamespace string            `json:"runtime_mount_namespace"`
	RuntimePIDNamespace   string            `json:"runtime_pid_namespace"`
	ApprovedBy            string            `json:"approved_by"`
	ApprovedAt            string            `json:"approved_at"`
}
