id: {{.ID}}
type: {{.Type}}
consistencyLevel: {{.ConsistencyLevel}}
owner:
  team: {{.OwnerTeam}}
  role: cell-owner
schema:
  primary: cell_{{.ID | replace "-" "_"}}
verify:
  smoke:
    - smoke.{{.ID}}.startup
l0Dependencies: []
