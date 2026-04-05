package meta

type Repository struct {
	Root       string
	Actors     map[string]Actor
	Assemblies []*AssemblyFile
	Cells      []*CellFile
	Contracts  []*ContractFile
	Journeys   []*JourneyFile
	Slices     []*SliceFile
	Status     *StatusBoardFile
}

type Actor struct {
	ID                  string `yaml:"id"`
	Type                string `yaml:"type"`
	MaxConsistencyLevel string `yaml:"maxConsistencyLevel"`
}

type AssemblyFile struct {
	Path     string
	DirID    string
	Assembly Assembly `yaml:",inline"`
}

type Assembly struct {
	ID    string   `yaml:"id"`
	Cells []string `yaml:"cells"`
	Build Build    `yaml:"build"`
}

type Build struct {
	EntryPoint     string `yaml:"entrypoint"`
	Binary         string `yaml:"binary"`
	DeployTemplate string `yaml:"deployTemplate"`
}

type CellFile struct {
	Path  string
	DirID string
	Cell  Cell `yaml:",inline"`
}

type Cell struct {
	ID               string         `yaml:"id"`
	Type             string         `yaml:"type"`
	ConsistencyLevel string         `yaml:"consistencyLevel"`
	Schema           Schema         `yaml:"schema"`
	Verify           CellVerify     `yaml:"verify"`
	L0Dependencies   []L0Dependency `yaml:"l0Dependencies"`
}

type Schema struct {
	Primary string `yaml:"primary"`
}

type CellVerify struct {
	Smoke []string `yaml:"smoke"`
}

type L0Dependency struct {
	Cell   string `yaml:"cell"`
	Reason string `yaml:"reason"`
}

type SliceFile struct {
	Path          string
	DirID         string
	ParentCellDir string
	Slice         Slice `yaml:",inline"`
}

type Slice struct {
	ID             string          `yaml:"id"`
	BelongsToCell  string          `yaml:"belongsToCell"`
	ContractUsages []ContractUsage `yaml:"contractUsages"`
	Verify         SliceVerify     `yaml:"verify"`
}

type ContractUsage struct {
	Contract string `yaml:"contract"`
	Role     string `yaml:"role"`
}

type SliceVerify struct {
	Unit     []string `yaml:"unit"`
	Contract []string `yaml:"contract"`
	Waivers  []Waiver `yaml:"waivers"`
}

type Waiver struct {
	Contract  string `yaml:"contract"`
	Owner     string `yaml:"owner"`
	Reason    string `yaml:"reason"`
	ExpiresAt string `yaml:"expiresAt"`
}

type ContractFile struct {
	Path       string
	KindDir    string
	VersionDir string
	Contract   Contract `yaml:",inline"`
}

type Contract struct {
	ID                string            `yaml:"id"`
	Kind              string            `yaml:"kind"`
	OwnerCell         string            `yaml:"ownerCell"`
	ConsistencyLevel  string            `yaml:"consistencyLevel"`
	Lifecycle         string            `yaml:"lifecycle"`
	Endpoints         ContractEndpoints `yaml:"endpoints"`
	SchemaRefs        map[string]string `yaml:"schemaRefs"`
	Replayable        *bool             `yaml:"replayable"`
	IdempotencyKey    string            `yaml:"idempotencyKey"`
	DeliverySemantics string            `yaml:"deliverySemantics"`
}

type ContractEndpoints struct {
	Server      string   `yaml:"server"`
	Clients     []string `yaml:"clients"`
	Publisher   string   `yaml:"publisher"`
	Subscribers []string `yaml:"subscribers"`
	Handler     string   `yaml:"handler"`
	Invokers    []string `yaml:"invokers"`
	Provider    string   `yaml:"provider"`
	Readers     []string `yaml:"readers"`
}

type JourneyFile struct {
	Path    string
	Journey Journey `yaml:",inline"`
}

type Journey struct {
	ID           string         `yaml:"id"`
	Goal         string         `yaml:"goal"`
	Cells        []string       `yaml:"cells"`
	Contracts    []string       `yaml:"contracts"`
	PassCriteria []PassCriteria `yaml:"passCriteria"`
}

type PassCriteria struct {
	Text     string `yaml:"text"`
	Mode     string `yaml:"mode"`
	CheckRef string `yaml:"checkRef"`
}

type StatusBoardFile struct {
	Path    string
	Entries []StatusEntry
}

type StatusEntry struct {
	JourneyID string `yaml:"journeyId"`
	State     string `yaml:"state"`
	Risk      string `yaml:"risk"`
	Blocker   string `yaml:"blocker"`
	UpdatedAt string `yaml:"updatedAt"`
}

func (f CellFile) EffectiveID() string {
	if f.Cell.ID != "" {
		return f.Cell.ID
	}
	return f.DirID
}

func (f SliceFile) EffectiveID() string {
	if f.Slice.ID != "" {
		return f.Slice.ID
	}
	return f.DirID
}

func (f SliceFile) EffectiveBelongsToCell() string {
	if f.Slice.BelongsToCell != "" {
		return f.Slice.BelongsToCell
	}
	return f.ParentCellDir
}

func (f ContractFile) EffectiveKind() string {
	if f.Contract.Kind != "" {
		return f.Contract.Kind
	}
	parts := splitContractID(f.Contract.ID)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func (f ContractFile) ProviderActor() string {
	switch f.EffectiveKind() {
	case "http":
		return f.Contract.Endpoints.Server
	case "event":
		return f.Contract.Endpoints.Publisher
	case "command":
		return f.Contract.Endpoints.Handler
	case "projection":
		return f.Contract.Endpoints.Provider
	default:
		return ""
	}
}

func (f ContractFile) ConsumerActors() []string {
	switch f.EffectiveKind() {
	case "http":
		return f.Contract.Endpoints.Clients
	case "event":
		return f.Contract.Endpoints.Subscribers
	case "command":
		return f.Contract.Endpoints.Invokers
	case "projection":
		return f.Contract.Endpoints.Readers
	default:
		return nil
	}
}

func (f ContractFile) EffectiveOwnerCell() string {
	if f.Contract.OwnerCell != "" {
		return f.Contract.OwnerCell
	}
	return f.ProviderActor()
}
