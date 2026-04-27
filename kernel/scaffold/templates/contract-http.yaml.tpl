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
  # FMT-25 also requires min/max facets for string and integer/number path/query
  # params, except `format: uuid` string params where UUID length is fixed.
  # The placeholder below intentionally fails `gocell validate --strict`
  # (path `/TODO/{id}` carries an `{id}` token with no pathParams declaration)
  # so the scaffold output advertises that it is not ready to ship.
  http:
    method: GET                       # GET | POST | PUT | PATCH | DELETE
    path: /api/v1/TODO/{id}           # TODO: replace with real route template
    # pathParams:                     # TODO: one entry per `{name}` in path
    #   id:
    #     type: string                # string | integer | number | boolean | uuid
    #     format: uuid                # optional hint; uuid is exempt from FMT-25 length facets
    # queryParams:                    # uncomment if the endpoint accepts query args
    #   cursor:
    #     type: string
    #     required: false
    #     minLength: 0
    #     maxLength: 256
    #   limit:
    #     type: integer
    #     required: false
    #     minimum: 1
    #     maximum: 500
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
