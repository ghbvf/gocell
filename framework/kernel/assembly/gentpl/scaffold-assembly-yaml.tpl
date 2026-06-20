id: {{.ID}}
cells:
{{- range .Cells}}
{{- if .Module}}
  - {id: {{.ID}}, module: {{.Module}}}
{{- else}}
  - {{.ID}}
{{- end}}
{{- end}}
owner:
  team: {{.OwnerTeam}}
  role: {{.OwnerRole}}
{{- if or .CompositionAPI .DeployTemplate}}
build:
{{- if .CompositionAPI}}
  compositionAPI: true
{{- end}}
{{- if .DeployTemplate}}
  deployTemplate: {{.DeployTemplate}}
{{- end}}
{{- end}}
