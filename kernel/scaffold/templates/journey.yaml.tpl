id: {{.ID}}
goal: "{{.Goal}}"
lifecycle: experimental
owner:
  team: {{.OwnerTeam}}
  role: journey-owner
cells:
{{- range .Cells}}
  - {{.}}
{{- end}}
contracts: []
passCriteria: []
