# Autohost Cloud API

REST API para gestión de nodos distribuidos y agentes, construida con Go y PostgreSQL.

## Stack

- **Go** 1.25+
- **PostgreSQL** 16
- **Router:** Chi v5
- **Migrations:** golang-migrate
- **Live reload:** Air

## Requisitos previos

- Go 1.25+
- Docker & Docker Compose
- [Air](https://github.com/air-verse/air) (solo para desarrollo con live reload): `go install github.com/air-verse/air@latest`

> **Nota:** tras instalar Air, asegúrate de que `~/go/bin` esté en tu `PATH`, o simplemente usa `make run-air` que ya resuelve la ruta automáticamente.

## Arranque rápido

### 1. Clonar el repositorio

```bash
git clone https://github.com/arturo/autohost-cloud-api.git
cd autohost-cloud-api
```

### 2. Configurar variables de entorno

Copia el archivo de ejemplo y edítalo:

```bash
cp .env.example .env
```

Variables disponibles:

```bash
PORT=8080
GRPC_PORT=9090
DATABASE_URL=postgres://autohost:autohost@localhost:5432/autohost?sslmode=disable
JWT_SECRET=supersecret            # cambiar en producción
ACCESS_TOKEN_TTL=15m
REFRESH_TOKEN_TTL=24h
FRONTEND_URL=http://localhost:3000

# OAuth — Google (opcional)
GOOGLE_CLIENT_ID=

# R2 Cloud Storage (opcional)
R2_ACCOUNT_ID=
R2_ACCESS_KEY_ID=
R2_SECRET_ACCESS_KEY=
R2_BUCKET_NAME=
```

### 3. Instalar dependencias

```bash
go mod download
```

### 4. Levantar la base de datos

```bash
docker compose up -d
```

Servicios que se inician:
- **PostgreSQL** en `localhost:5432`
- **pgAdmin** en `http://localhost:5050` (opcional)

### 5. Ejecutar migraciones

```bash
make migrate-up
```

### 6. Ejecutar la aplicación

**Modo monolítico (REST + gRPC juntos en un proceso):**
```bash
make dev
```

**Modo desacoplado (idéntico a producción):**
- **Terminal 1 — REST API / Cloud Run (:8080):**
  ```bash
  make dev-rest
  ```
- **Terminal 2 — gRPC Server / Compute Engine (:9090):**
  ```bash
  make dev-grpc
  ```
- **O ambos en paralelo en una sola terminal:**
  ```bash
  make dev-split
  ```

La API REST estará en `http://localhost:8080` y el servidor gRPC en `localhost:9090`.

---

## Estructura del proyecto

```
.
├── cmd/
│   ├── api/              # Entry point principal (servidores REST y gRPC)
│   └── migrate/          # Runner de migraciones de base de datos
├── internal/
│   ├── app/              # Composición de módulos de inyección de dependencias (Uber Fx)
│   ├── core/             # Dominio y casos de uso organizados en Bounded Contexts
│   │   ├── apps/         # Proyectos, marketplace, Caddy reverse proxy, backups, monitores
│   │   ├── identity/     # Autenticación, usuarios, planes, settings, bots, notificaciones
│   │   ├── infra/        # Clusters de nodos, máquinas virtuales KVM/QEMU
│   │   ├── jobs/         # Despacho de jobs remotos, comandos de nodo y tareas programadas
│   │   ├── network/      # Red mesh VPN (WireGuard) y estadísticas de tráfico
│   │   └── node/         # Ciclo de vida del nodo, tokens, métricas, logs, diagnósticos y alertas
│   ├── dispatcher/       # Orquestador desacoplado para despacho y enrutamiento de jobs (HTTP/gRPC/Scheduler)
│   ├── grpc/             # Servidor y servicios gRPC para comunicación con agentes remotos
│   ├── handler/          # Controladores HTTP REST y middlewares (Chi v5)
│   ├── repository/       # Implementaciones de persistencia en PostgreSQL (`internal/repository/postgres/`)
│   ├── scheduler/        # Programadores de tareas en background y limpieza periódica
│   ├── server/           # Configuración del servidor HTTP, enrutador y ciclo de vida Fx
│   └── platform/         # Infraestructura transversal (JWT, hashing, R2/S3 storage, email, logging)
├── migrations/           # Archivos SQL de migraciones (golang-migrate)
├── request/              # Colección de peticiones HTTP de prueba (.http)
├── compose.yml
└── Makefile
```

---

## API Endpoints

### Health

| Método | Ruta | Descripción |
|--------|------|-------------|
| `GET` | `/health` | Estado del servicio |

### Auth

| Método | Ruta | Auth | Descripción |
|--------|------|------|-------------|
| `POST` | `/v1/auth/register` | — | Registrar usuario |
| `POST` | `/v1/auth/login` | — | Iniciar sesión |
| `POST` | `/v1/auth/refresh` | — | Refrescar access token |
| `POST` | `/v1/auth/logout` | — | Cerrar sesión |
| `GET` | `/v1/auth/me` | JWT | Usuario actual |

### Nodos

| Método | Ruta | Auth | Descripción |
|--------|------|------|-------------|
| `GET` | `/v1/nodes` | JWT | Listar nodos del usuario |
| `GET` | `/v1/nodes/with-metrics` | JWT | Listar nodos con últimas métricas |

### Enrollment (incorporación de agentes)

| Método | Ruta | Auth | Descripción |
|--------|------|------|-------------|
| `POST` | `/v1/enrollments/generate` | JWT | Generar token de enrollment |
| `POST` | `/v1/enrollments/enroll` | — | Registrar nodo con token |

### Heartbeats (autenticación por node token)

| Método | Ruta | Auth | Descripción |
|--------|------|------|-------------|
| `POST` | `/v1/heartbeats/heartbeat` | Node token | Enviar heartbeat |

### Métricas de nodo (autenticación por node token)

| Método | Ruta | Auth | Descripción |
|--------|------|------|-------------|
| `POST` | `/v1/node-metrics/metrics` | Node token | Enviar métricas del nodo |

---

## Comandos Make

```bash
make run              # Ejecutar la aplicación
make run-air          # Levantar docker compose + live reload con Air
make build            # Compilar binario en bin/
make test             # Ejecutar tests
make docker           # Construir imagen Docker
make migrate-up       # Aplicar migraciones pendientes
make migrate-down     # Revertir última migración
make migrate-version  # Ver versión de migración actual
```

---

## Base de datos

### Acceso directo

```bash
docker exec -it ahcl-postgres psql -U autohost -d autohost
```

### pgAdmin

Disponible en `http://localhost:5050`:
- **Email:** `admin@localhost.com`
- **Password:** `devpass`

Conexión:
- **Host:** `postgres` · **Port:** `5432`
- **Database:** `autohost` · **User/Pass:** `autohost`

---

## Arquitectura

El proyecto sigue los principios de **Clean Architecture** combinados con **Bounded Contexts (DDD)** y **Consumer-Driven Interfaces**:

1. **Core Domain (`internal/core/`)**:
   - Totalmente desacoplado de frameworks web y librerías externas de persistencia.
   - Organizado en Bounded Contexts cohesivos: `apps`, `identity`, `infra`, `jobs`, `network`, y `node`.
   - Contiene entidades de dominio, DTOs, interfaces de repositorio y servicios de negocio.
2. **Dispatcher (`internal/dispatcher/`)**:
   - Capa desacoplada para el despacho de jobs y comandos hacia agentes conectados (vía gRPC o persistencia reactiva).
   - Invocable indistintamente por controladores REST, tareas del scheduler o servicios internos.
3. **Repository (`internal/repository/postgres/`)**:
   - Implementaciones concretas de acceso a datos sobre PostgreSQL utilizando `pgx`.
4. **Primary Adapters (`internal/handler/` & `internal/grpc/`)**:
   - Handlers REST bajo Chi v5 y servicios gRPC para agentes remotos.
5. **Platform (`internal/platform/`)**:
   - Utilidades técnicas e infraestructura transversal (JWT, Argon2/Bcrypt, Cloudflare R2, clientes SMTP).

Para agregar una funcionalidad nueva:
1. Definir entidad, interfaces de repositorio y servicio dentro del subpaquete correspondiente en `internal/core/<bounded-context>/`.
2. Implementar los métodos de persistencia en `internal/repository/postgres/`.
3. Exponer los endpoints en `internal/handler/` o `internal/grpc/`.
4. Registrar los constructores en los módulos Uber Fx (`internal/app/modules.go`).
5. Crear la migración SQL correspondiente si hay cambios de esquema (`make migrate-create`).


## Licencia / License

Este proyecto es software libre y de código abierto (Open Source), distribuido bajo la licencia **[GNU Affero General Public License v3.0 (AGPLv3)](LICENSE)**.

Eres libre de usarlo, modificarlo y hospedar tu propia instancia (self-hosted) de forma gratuita. De acuerdo con los términos de AGPLv3, si modificas el software y ofreces servicios a través de una red, debes poner a disposición el código fuente correspondiente bajo la misma licencia.

Para licencias comerciales alternativas o consultas sobre la versión SaaS gestionada, contacta al equipo de AutoHost.
