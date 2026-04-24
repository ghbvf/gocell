id: {{.ID}}
kind: http
ownerCell: {{.OwnerCell}}
consistencyLevel: L1
lifecycle: draft
endpoints:
  server: {{.OwnerCell}}
  clients: []
  # TODO: declare the transport layer — FMT-13 enforces path ↔ pathParams
  # consistency once endpoints.http is present. Every `{name}` token in `path`
  # must appear as a pathParams key with a typed `type:`; queryParams are
  # declared only for query-string arguments (not path placeholders).
  http:
    method: GET                       # GET | POST | PUT | PATCH | DELETE
    path: /api/v1/TODO/{id}           # 2xx success, path placeholders use {name}
    pathParams:
      id:
        type: string                  # string | integer | number | boolean | uuid
        format: uuid                  # optional hint (uuid, date-time, int64, …)
    # queryParams:                    # uncomment if the endpoint accepts query args
    #   cursor:
    #     type: string
    #     required: false
    successStatus: 200
    noContent: false
    # responses:                      # uncomment to declare error responses
    #   401:
    #     description: "Unauthorized — missing or invalid bearer token"
    #     schemaRef: "../../../../shared/errors/error-response-v1.schema.json"
schemaRefs:
  request: request.schema.json
  response: response.schema.json
