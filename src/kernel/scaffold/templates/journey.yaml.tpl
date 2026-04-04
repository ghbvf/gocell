id: {{.ID}}
goal: {{.Goal}}
owner:
  team: {{.OwnerTeam}}
  role: journey-owner
cells:
{{- range .Cells}}
  - {{.}}
{{- end}}
contracts: []
passCriteria: []
