package journey

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	ecErr "github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildTestProject returns a ProjectMeta with 3 journeys:
//   - J-useronboarding: single cell (accesscore)
//   - J-ssologin: cross-cell (accesscore, auditcore, configcore)
//   - J-auditlogintrail: cross-cell (accesscore, auditcore)
//
// and 2 status-board entries (J-ssologin, J-auditlogintrail).
func buildTestProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-useronboarding": {
				ID:        "J-useronboarding",
				Goal:      "new user onboarding",
				Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells:     []string{"accesscore"},
				Contracts: []string{"event.user.created.v1"},
				PassCriteria: []metadata.PassCriterion{
					{Text: "user record created", Mode: "auto"},
				},
			},
			"J-ssologin": {
				ID:        "J-ssologin",
				Goal:      "SSO login with session",
				Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells:     []string{"accesscore", "auditcore", "configcore"},
				Contracts: []string{"http.auth.login.v1", "event.session.created.v1"},
				PassCriteria: []metadata.PassCriterion{
					{Text: "OIDC redirect done", Mode: "auto"},
					{Text: "session persisted", Mode: "auto"},
				},
			},
			"J-auditlogintrail": {
				ID:        "J-auditlogintrail",
				Goal:      "login events propagate to audit hash chain",
				Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells:     []string{"accesscore", "auditcore"},
				Contracts: []string{"event.session.created.v1", "event.audit.integrity-verified.v1"},
				PassCriteria: []metadata.PassCriterion{
					{Text: "event consumed by auditcore", Mode: "auto"},
					{Text: "hash chain integrity verified", Mode: "auto"},
				},
			},
		},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-ssologin", State: "doing", Risk: "low", UpdatedAt: "2026-04-04"},
			{JourneyID: "J-auditlogintrail", State: "todo", Risk: "low", UpdatedAt: "2026-04-05"},
		},
	}
}

func TestNewCatalog(t *testing.T) {
	tests := []struct {
		name          string
		project       *metadata.ProjectMeta
		wantJourneys  int
		wantStatusLen int
	}{
		{
			name:          "nil ProjectMeta produces empty catalog",
			project:       nil,
			wantJourneys:  0,
			wantStatusLen: 0,
		},
		{
			name: "zero-value ProjectMeta produces empty catalog",
			project: &metadata.ProjectMeta{
				Journeys: nil,
			},
			wantJourneys:  0,
			wantStatusLen: 0,
		},
		{
			name:          "populated ProjectMeta",
			project:       buildTestProject(),
			wantJourneys:  3,
			wantStatusLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCatalog(tt.project)
			require.NotNil(t, c)
			assert.Equal(t, tt.wantJourneys, c.Count())

			// verify status board entries are indexed
			statusCount := 0
			if tt.project != nil {
				for _, e := range tt.project.StatusBoard {
					if c.Status(e.JourneyID) != nil {
						statusCount++
					}
				}
			}
			assert.Equal(t, tt.wantStatusLen, statusCount)
		})
	}
}

func TestGet(t *testing.T) {
	c := NewCatalog(buildTestProject())

	tests := []struct {
		name   string
		id     string
		wantNil bool
		wantID string
	}{
		{name: "existing journey", id: "J-ssologin", wantNil: false, wantID: "J-ssologin"},
		{name: "non-existing journey", id: "J-does-not-exist", wantNil: true},
		{name: "empty id", id: "", wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := c.Get(tt.id)
			if tt.wantNil {
				assert.Nil(t, j)
			} else {
				require.NotNil(t, j)
				assert.Equal(t, tt.wantID, j.ID)
			}
		})
	}
}

func TestList(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
		wantIDs []string
	}{
		{
			name:    "sorted by ID",
			project: buildTestProject(),
			wantIDs: []string{"J-auditlogintrail", "J-ssologin", "J-useronboarding"},
		},
		{
			name:    "empty catalog",
			project: nil,
			wantIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCatalog(tt.project)
			list := c.List()
			ids := make([]string, len(list))
			for i, j := range list {
				ids[i] = j.ID
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestCellJourneys(t *testing.T) {
	c := NewCatalog(buildTestProject())

	tests := []struct {
		name    string
		cellID  string
		wantIDs []string
	}{
		{
			name:    "accesscore referenced by all three journeys",
			cellID:  "accesscore",
			wantIDs: []string{"J-auditlogintrail", "J-ssologin", "J-useronboarding"},
		},
		{
			name:    "auditcore referenced by two journeys",
			cellID:  "auditcore",
			wantIDs: []string{"J-auditlogintrail", "J-ssologin"},
		},
		{
			name:    "configcore referenced by one journey",
			cellID:  "configcore",
			wantIDs: []string{"J-ssologin"},
		},
		{
			name:    "unknown cell returns empty",
			cellID:  "nonexistent",
			wantIDs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.CellJourneys(tt.cellID)
			ids := make([]string, len(result))
			for i, j := range result {
				ids[i] = j.ID
			}
			if tt.wantIDs == nil {
				assert.Empty(t, ids)
			} else {
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestContractJourneys(t *testing.T) {
	c := NewCatalog(buildTestProject())

	tests := []struct {
		name       string
		contractID string
		wantIDs    []string
	}{
		{
			name:       "event.session.created.v1 referenced by two journeys",
			contractID: "event.session.created.v1",
			wantIDs:    []string{"J-auditlogintrail", "J-ssologin"},
		},
		{
			name:       "http.auth.login.v1 referenced by one journey",
			contractID: "http.auth.login.v1",
			wantIDs:    []string{"J-ssologin"},
		},
		{
			name:       "event.user.created.v1 referenced by one journey",
			contractID: "event.user.created.v1",
			wantIDs:    []string{"J-useronboarding"},
		},
		{
			name:       "unknown contract returns empty",
			contractID: "nonexistent",
			wantIDs:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.ContractJourneys(tt.contractID)
			ids := make([]string, len(result))
			for i, j := range result {
				ids[i] = j.ID
			}
			if tt.wantIDs == nil {
				assert.Empty(t, ids)
			} else {
				assert.Equal(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	c := NewCatalog(buildTestProject())

	tests := []struct {
		name      string
		journeyID string
		wantNil   bool
		wantState string
	}{
		{
			name:      "existing status entry",
			journeyID: "J-ssologin",
			wantNil:   false,
			wantState: "doing",
		},
		{
			name:      "another existing status entry",
			journeyID: "J-auditlogintrail",
			wantNil:   false,
			wantState: "todo",
		},
		{
			name:      "journey without status entry",
			journeyID: "J-useronboarding",
			wantNil:   true,
		},
		{
			name:      "nonexistent journey",
			journeyID: "J-nonexistent",
			wantNil:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := c.Status(tt.journeyID)
			if tt.wantNil {
				assert.Nil(t, s)
			} else {
				require.NotNil(t, s)
				assert.Equal(t, tt.wantState, s.State)
			}
		})
	}
}

func TestCrossCellJourneys(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
		wantIDs []string
	}{
		{
			name:    "returns only cross-cell journeys sorted by ID",
			project: buildTestProject(),
			wantIDs: []string{"J-auditlogintrail", "J-ssologin"},
		},
		{
			name:    "empty catalog",
			project: nil,
			wantIDs: []string{},
		},
		{
			name: "all single-cell journeys",
			project: &metadata.ProjectMeta{
				Journeys: map[string]*metadata.JourneyMeta{
					"J-solo": {
						ID:    "J-solo",
						Cells: []string{"only-one"},
					},
				},
			},
			wantIDs: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCatalog(tt.project)
			result := c.CrossCellJourneys()
			ids := make([]string, len(result))
			for i, j := range result {
				ids[i] = j.ID
			}
			assert.Equal(t, tt.wantIDs, ids)
		})
	}
}

func TestCount(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
		want    int
	}{
		{name: "nil project", project: nil, want: 0},
		{name: "populated project", project: buildTestProject(), want: 3},
		{
			name: "single journey",
			project: &metadata.ProjectMeta{
				Journeys: map[string]*metadata.JourneyMeta{
					"J-one": {ID: "J-one", Cells: []string{"a"}},
				},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCatalog(tt.project)
			assert.Equal(t, tt.want, c.Count())
		})
	}
}

func TestEmptyProjectMeta_NoPanic(t *testing.T) {
	// Ensure various zero/empty states don't panic.
	projects := []*metadata.ProjectMeta{
		nil,
		{},
		{Journeys: nil, StatusBoard: nil},
		{Journeys: map[string]*metadata.JourneyMeta{}, StatusBoard: []metadata.StatusBoardEntry{}},
	}

	for i, pm := range projects {
		c := NewCatalog(pm)
		require.NotNil(t, c, "case %d", i)
		assert.Equal(t, 0, c.Count(), "case %d", i)
		assert.Empty(t, c.List(), "case %d", i)
		assert.Nil(t, c.Get("any"), "case %d", i)
		assert.Nil(t, c.Status("any"), "case %d", i)
		assert.Empty(t, c.CellJourneys("any"), "case %d", i)
		assert.Empty(t, c.ContractJourneys("any"), "case %d", i)
		assert.Empty(t, c.CrossCellJourneys(), "case %d", i)
	}
}

func TestValidate(t *testing.T) {
	allCells := map[string]struct{}{
		"accesscore": {},
		"auditcore":  {},
		"configcore": {},
	}
	allContracts := map[string]struct{}{
		"event.user.created.v1":            {},
		"http.auth.login.v1":               {},
		"event.session.created.v1":         {},
		"event.audit.integrity-verified.v1": {},
	}

	tests := []struct {
		name        string
		project     *metadata.ProjectMeta
		cellIDs     map[string]struct{}
		contractIDs map[string]struct{}
		wantErr     bool
		wantCode    ecErr.Code
		wantContain []string // substrings expected in error message
	}{
		{
			name:        "all references valid",
			project:     buildTestProject(),
			cellIDs:     allCells,
			contractIDs: allContracts,
			wantErr:     false,
		},
		{
			name:    "missing cell reference",
			project: buildTestProject(),
			cellIDs: map[string]struct{}{
				"accesscore": {},
				"auditcore":  {},
				// configcore missing
			},
			contractIDs: allContracts,
			wantErr:     true,
			wantCode:    ecErr.ErrReferenceBroken,
			wantContain: []string{"configcore", "unknown cell"},
		},
		{
			name:    "missing contract reference",
			project: buildTestProject(),
			cellIDs: allCells,
			contractIDs: map[string]struct{}{
				"event.user.created.v1":    {},
				"http.auth.login.v1":       {},
				"event.session.created.v1": {},
				// event.audit.integrity-verified.v1 missing
			},
			wantErr:     true,
			wantCode:    ecErr.ErrReferenceBroken,
			wantContain: []string{"event.audit.integrity-verified.v1", "unknown contract"},
		},
		{
			name:        "empty catalog validates successfully",
			project:     nil,
			cellIDs:     nil,
			contractIDs: nil,
			wantErr:     false,
		},
		{
			name:        "nil sets treat all references as broken",
			project:     buildTestProject(),
			cellIDs:     nil,
			contractIDs: nil,
			wantErr:     true,
			wantCode:    ecErr.ErrReferenceBroken,
		},
		{
			name:        "empty sets treat all references as broken",
			project:     buildTestProject(),
			cellIDs:     map[string]struct{}{},
			contractIDs: map[string]struct{}{},
			wantErr:     true,
			wantCode:    ecErr.ErrReferenceBroken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCatalog(tt.project)
			err := c.Validate(tt.cellIDs, tt.contractIDs)
			if !tt.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ec *ecErr.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, tt.wantCode, ec.Code)
			for _, sub := range tt.wantContain {
				assert.Contains(t, err.Error(), sub)
			}
		})
	}
}
