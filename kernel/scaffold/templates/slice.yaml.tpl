id: {{.ID}}
belongsToCell: {{.CellID}}
contractUsages: []
verify:
  unit:
    - unit.{{.ID}}.service
  contract: []
  waivers: []

allowedFiles:
  - cells/{{.CellID}}/slices/{{.ID}}/**
