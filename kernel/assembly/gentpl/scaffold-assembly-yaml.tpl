id: {{.ID}}
cells:
{{- range .Cells}}
  - {{.}}
{{- end}}
owner:
  team: {{.OwnerTeam}}
  role: {{.OwnerRole}}
{{- if .DeployTemplate}}
build:
  deployTemplate: {{.DeployTemplate}}
{{- end}}
