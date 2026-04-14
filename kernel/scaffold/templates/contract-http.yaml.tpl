id: {{.ID}}
kind: http
ownerCell: {{.OwnerCell}}
consistencyLevel: L1
lifecycle: draft
endpoints:
  server: {{.OwnerCell}}
  clients: []
schemaRefs:
  request: request.schema.json
  response: response.schema.json
