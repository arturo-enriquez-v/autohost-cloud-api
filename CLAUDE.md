# autohost-cloud-api

Plano de control de AutoHost (ver `../CLAUDE.md` para el workspace). Go 1.25, Chi v5, PostgreSQL 16,
golang-migrate. Módulo: `github.com/arturo/autohost-cloud-api`.

## Comandos

```bash
make dev                 # Postgres (docker compose) + REST :8080 + gRPC :9090 con live reload (Air)
make dev-rest|dev-grpc   # solo uno de los dos (como en Cloud Run / VPS)
go test ./...            # todos los tests (no necesitan base de datos)
go test ./internal/server -count=1   # cobertura de la spec, $refs y requireNodePerm en rutas de nodo
go vet ./...
make migrate-up          # aplicar migraciones
make proto               # regenerar gRPC desde proto/ (y actualizar la copia del agente)
npx --no-install @redocly/cli lint api/openapi.yaml       # lint de la spec (excepciones en .redocly.lint-ignore.yaml)
make mcp-update          # valida la spec y la publica en Scalar: actualiza el MCP (VERSION=x.y.z, por defecto el siguiente parche)
```

`SERVICE_MODE` = `all` | `http` | `grpc`. `API_DOCS_ENABLED=true` sirve `/docs` (Scalar) y `/openapi.yaml`.
`/mcp` (MCP para asistentes, `MCP_ENABLED`, activo por defecto) sale de la spec: un endpoint documentado ya es
una operación disponible por MCP, salvo los que `internal/mcpserver/index.go` bloquea (streaming, binarios,
sesión, rutas de agente o públicas).

## Estructura

```
cmd/api, cmd/migrate        entrypoints
internal/server             router.go (todas las rutas), http/grpc, docs, tests de rutas
internal/handler            handlers HTTP (uno por recurso), node_authz.go, input_validation.go
internal/handler/middleware Auth (JWT), node token, external log token, rate limit, origin
internal/handler/response   RespondJSON / RespondError
internal/core/<dominio>     lógica de negocio: identity, node, jobs, apps, infra, network
internal/repository/postgres acceso a datos (sqlx)
internal/grpc               servidor gRPC de agentes (stream Connect, resultados de jobs)
internal/dispatcher         envío de jobs a nodos conectados
internal/mcpserver          servidor MCP en /mcp, generado desde la spec (search/schema/execute por el router)
internal/core/assistant     chat del panel: bucle con un modelo compatible con OpenAI y herramientas que
                            ejecutan la API con la sesión del usuario; las escrituras esperan aprobación
internal/app                wiring de módulos y BD
migrations/                 NNNN_nombre.up.sql / .down.sql, numeración secuencial
api/openapi.yaml            spec OpenAPI 3.1, embebida en el binario (api/api.go)
api/grpc.md                 guía del gRPC con el agente (conexión, auth, flujos); referencia en proto/
```

Flujo: handler (auth, permisos, parseo, códigos HTTP) → servicio en `core` (reglas) → repositorio (SQL).
Los handlers no hablan con la BD directamente.

## Reglas para endpoints

- **Autorización de nodo**: toda ruta con `{nodeId}` en la URL usa `requireNodePerm(perm)` a nivel de ruta.
  `internal/server/route_authz_test.go` falla si no; las excepciones van en `nodeRouteExceptions` con su
  motivo. Los handlers repiten la comprobación (`checkNodeAccess` / `checkNodeIDAccess`) como defensa en
  profundidad. Un nodo inexistente es 403, no 404, para quien no es admin.
- **Permisos**: usar las constantes `identity.Perm*`. Acciones que dan control del host (ejecutar comandos,
  compose arbitrario, recrear contenedores) exigen `terminal:exec` o equivalente, no solo `*:manage`.
- **Recursos de usuario** (no de nodo): filtrar siempre por `user_id` en el repositorio; nunca confiar en un
  ID del cuerpo o de la URL sin comprobar el dueño (IDOR).
- **Errores**: no devolver `err.Error()` de errores internos al cliente; loguear con `log.Printf("[ERROR] ...")`
  y responder un mensaje fijo. Distinguir 400 / 403 / 404 / 409 / 500 con `errors.Is` sobre errores
  centinela del `core`. En código nuevo, errores JSON con `response.RespondError` (`{"error": "..."}`);
  el código antiguo usa `http.Error` en texto plano y se migra poco a poco.
- **Entrada**: validar en el handler (o en `input_validation.go`) antes de llegar al servicio; el body ya está
  limitado a 2 MB salvo subidas.
- **Agentes**: las rutas de agente usan el middleware de token de nodo y toman el nodo del token, nunca del
  cuerpo.

## OpenAPI

Toda ruta del router debe estar en `api/openapi.yaml`: `TestOpenAPICoversRouter` falla si falta una o si la
spec documenta una que ya no existe, y `TestOpenAPIRefsResolve` comprueba los `$ref`. Al añadir o cambiar
un endpoint, actualizar la spec en el mismo commit.

Estilo de la spec: descripciones cortas en inglés que dicen el permiso necesario y los efectos; documentar
los códigos y el formato de error reales (texto plano o JSON) aunque sean incoherentes, y anotar la
incoherencia en PENDIENTES; reutilizar `components/responses` (`Unauthorized`, `Forbidden`, `NodeNotFound`,
`InternalError`, `*JSON`...) y `components/parameters`; `operationId` en camelCase y único; los strings con
`: ` o que empiezan por `` ` `` van entre comillas.

## gRPC

El contrato con el agente es `proto/node_agent/v1/node_agent.proto`, con cada mensaje y campo comentado;
`api/grpc.md` explica conexión, autenticación y flujos. Al cambiarlo: comentar los campos nuevos, seguir
las reglas de compatibilidad de `api/grpc.md`, `make proto`, copiar `internal/grpc/nodepb/*.pb.go` a
`../autohost-agent/internal/transport/nodepb/` y actualizar `api/grpc.md` si cambia un flujo.

## Tests

- Tests de handler con dependencias falsas en el mismo paquete (`*_test.go` junto al handler); ver
  `bola_isolation_test.go` y `*_authz_test.go` para comprobar aislamiento entre usuarios.
- Un fix de seguridad viene con un test que falla sin el fix.

## Migraciones

Siguiente número = último en `migrations/` + 1, siempre con `.up.sql` y `.down.sql`. Usar
`IF NOT EXISTS` / `IF EXISTS`. Nunca editar una migración ya publicada.
