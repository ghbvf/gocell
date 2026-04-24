id: {{.ID}}
kind: http
ownerCell: {{.OwnerCell}}
consistencyLevel: L1
lifecycle: draft
endpoints:
  server: {{.OwnerCell}}
  clients: []
  # TODO: fill in the transport layer. FMT-13 enforces path ↔ pathParams
  # consistency once `endpoints.http` is present, so every `{name}` token in
  # `path` must appear as a pathParams key with a typed `type:`.
  # The placeholder below intentionally fails `gocell validate --strict`
  # (path `/TODO/{id}` carries an `{id}` token with no pathParams declaration)
  # so the scaffold output advertises that it is not ready to ship.
  http:
    method: GET                       # GET | POST | PUT | PATCH | DELETE
    path: /api/v1/TODO/{id}           # TODO: replace with real route template
    # pathParams:                     # TODO: one entry per `{name}` in path
    #   id:
    #     type: string                # string | integer | number | boolean | uuid
    #     format: uuid                # optional hint (uuid, date-time, int64, …)
    # queryParams:                    # uncomment if the endpoint accepts query args
    #   cursor:
    #     type: string
    #     required: false
    successStatus: 200
    noContent: false
    # responses:                      # REQUIRED when endpoint is not Public=true —
    #   401:                          # declare 401 for missing/invalid bearer token,
    #     description: "Unauthorized — missing or invalid bearer token"
    #     schemaRef: "../../../../shared/errors/error-response-v1.schema.json"
    #   403:                          # and 403 when the Policy can deny an
    #     description: "Forbidden — authenticated but lacks required role"
    #     schemaRef: "../../../../shared/errors/error-response-v1.schema.json"
    #                                 # authenticated caller (AnyRole, SelfOr, ...).
schemaRefs:
  request: request.schema.json
  response: response.schema.json
