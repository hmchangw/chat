# Plan 10: CI/CD & Local Dev Infrastructure

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Create Azure Pipelines CI/CD configuration and per-service local development docker-compose files. Each service's Dockerfile is created as part of its own plan (Plans 2-9); this plan creates the pipeline and local dev orchestration.

**Depends on:** Plans 1-9 (all services must be built)

---

## File Structure

| Action | Path | Responsibility |
|--------|------|----------------|
| Create | `azure-pipelines.yml` | Multi-stage CI/CD pipeline |
| Create | `build/auth-service/docker-compose.yml` | Auth service + NATS |
| Create | `build/message-worker/docker-compose.yml` | Message worker + NATS + MongoDB + Cassandra |
| Create | `build/broadcast-worker/docker-compose.yml` | Broadcast worker + NATS + MongoDB |
| Create | `build/notification-worker/docker-compose.yml` | Notification worker + NATS + MongoDB |
| Create | `build/room-gatekeeper/docker-compose.yml` | Room gatekeeper + NATS + MongoDB |
| Create | `build/room-worker/docker-compose.yml` | Room worker + NATS + MongoDB |
| Create | `build/inbox-worker/docker-compose.yml` | Inbox worker + NATS + MongoDB |
| Create | `build/history-service/docker-compose.yml` | History service + NATS + MongoDB + Cassandra |

---

### Task 1: `azure-pipelines.yml` — CI/CD pipeline

- [ ] **Step 1: Create `azure-pipelines.yml`**

```yaml
trigger:
  branches:
    include:
      - main
      - develop

pr:
  branches:
    include:
      - main

variables:
  GO_VERSION: '1.25'
  REGISTRY: '$(containerRegistry)'
  SERVICES: >-
    auth-service
    message-worker
    broadcast-worker
    notification-worker
    room-gatekeeper
    room-worker
    inbox-worker
    history-service

stages:
  - stage: Validate
    displayName: 'Lint & Test'
    jobs:
      - job: LintAndTest
        pool:
          vmImage: 'ubuntu-latest'
        steps:
          - task: GoTool@0
            inputs:
              version: '$(GO_VERSION)'
            displayName: 'Install Go $(GO_VERSION)'

          - script: |
              go vet ./...
            displayName: 'Go Vet'

          - script: |
              go test ./pkg/... -v -race -coverprofile=coverage-pkg.out
            displayName: 'Test shared packages'

          - script: |
              for svc in auth-service message-worker broadcast-worker notification-worker room-gatekeeper room-worker inbox-worker history-service; do
                echo "=== Testing $svc ==="
                go test ./$svc/... -v -race -coverprofile=coverage-$svc.out || exit 1
              done
            displayName: 'Test all services'

          - script: |
              for svc in auth-service message-worker broadcast-worker notification-worker room-gatekeeper room-worker inbox-worker history-service; do
                echo "=== Building $svc ==="
                go build -o /dev/null ./$svc/ || exit 1
              done
            displayName: 'Build all services'

          - task: PublishCodeCoverageResults@2
            inputs:
              summaryFileLocation: 'coverage-pkg.out'
              pathToSources: '$(Build.SourcesDirectory)'
            displayName: 'Publish coverage'
            condition: succeededOrFailed()

  - stage: Build
    displayName: 'Build & Push Images'
    dependsOn: Validate
    condition: and(succeeded(), eq(variables['Build.SourceBranch'], 'refs/heads/main'))
    jobs:
      - job: BuildImages
        pool:
          vmImage: 'ubuntu-latest'
        strategy:
          matrix:
            auth-service:
              SERVICE_NAME: auth-service
            message-worker:
              SERVICE_NAME: message-worker
            broadcast-worker:
              SERVICE_NAME: broadcast-worker
            notification-worker:
              SERVICE_NAME: notification-worker
            room-gatekeeper:
              SERVICE_NAME: room-gatekeeper
            room-worker:
              SERVICE_NAME: room-worker
            inbox-worker:
              SERVICE_NAME: inbox-worker
            history-service:
              SERVICE_NAME: history-service
        steps:
          - task: Docker@2
            inputs:
              containerRegistry: '$(containerRegistry)'
              repository: 'chat/$(SERVICE_NAME)'
              command: 'buildAndPush'
              Dockerfile: '$(SERVICE_NAME)/Dockerfile'
              buildContext: '.'
              tags: |
                $(Build.BuildId)
                latest
            displayName: 'Build & push $(SERVICE_NAME)'
```

- [ ] **Step 2: Verify YAML is valid**

```bash
python3 -c "import yaml; yaml.safe_load(open('azure-pipelines.yml'))"
```

- [ ] **Step 3: Commit**

```bash
git add azure-pipelines.yml && git commit -m "ci: add Azure Pipelines multi-stage pipeline for all services"
```

---

### Task 2: Per-service local dev docker-compose files

Each docker-compose file mounts the service's Dockerfile (built from repo root context) and includes only the infrastructure that service requires.

- [ ] **Step 1: Create `build/auth-service/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  auth-service:
    build:
      context: ../..
      dockerfile: auth-service/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - NATS_CREDS=/run/secrets/nats.creds
      - AUTH_SIGNING_KEY=${AUTH_SIGNING_KEY}
    depends_on:
      - nats
```

- [ ] **Step 2: Create `build/message-worker/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  cassandra:
    image: cassandra:5
    ports:
      - "9042:9042"
    environment:
      - CASSANDRA_CLUSTER_NAME=chat-dev

  message-worker:
    build:
      context: ../..
      dockerfile: message-worker/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - CASSANDRA_HOSTS=cassandra
      - CASSANDRA_KEYSPACE=chat
    depends_on:
      - nats
      - mongodb
      - cassandra
```

- [ ] **Step 3: Create `build/broadcast-worker/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  broadcast-worker:
    build:
      context: ../..
      dockerfile: broadcast-worker/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 4: Create `build/notification-worker/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  notification-worker:
    build:
      context: ../..
      dockerfile: notification-worker/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 5: Create `build/room-gatekeeper/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  room-gatekeeper:
    build:
      context: ../..
      dockerfile: room-gatekeeper/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - MAX_ROOM_SIZE=1000
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 6: Create `build/room-worker/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  room-worker:
    build:
      context: ../..
      dockerfile: room-worker/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 7: Create `build/inbox-worker/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  inbox-worker:
    build:
      context: ../..
      dockerfile: inbox-worker/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
    depends_on:
      - nats
      - mongodb
```

- [ ] **Step 8: Create `build/history-service/docker-compose.yml`**

```yaml
services:
  nats:
    image: nats:2.11-alpine
    ports:
      - "4222:4222"
      - "8222:8222"
    command: ["--jetstream", "--http_port", "8222"]

  mongodb:
    image: mongo:8
    ports:
      - "27017:27017"

  cassandra:
    image: cassandra:5
    ports:
      - "9042:9042"
    environment:
      - CASSANDRA_CLUSTER_NAME=chat-dev

  history-service:
    build:
      context: ../..
      dockerfile: history-service/Dockerfile
    environment:
      - NATS_URL=nats://nats:4222
      - SITE_ID=site-local
      - MONGO_URI=mongodb://mongodb:27017
      - MONGO_DB=chat
      - CASSANDRA_HOSTS=cassandra
      - CASSANDRA_KEYSPACE=chat
    depends_on:
      - nats
      - mongodb
      - cassandra
```

- [ ] **Step 9: Commit**

```bash
git add build/ && git commit -m "infra: add per-service docker-compose files for local development"
```

---

### Task 3: Verify all Dockerfiles are consistent

- [ ] **Step 1: Check each service has a Dockerfile**

```bash
for svc in auth-service message-worker broadcast-worker notification-worker room-gatekeeper room-worker inbox-worker history-service; do
  if [ ! -f "$svc/Dockerfile" ]; then
    echo "MISSING: $svc/Dockerfile"
  else
    echo "OK: $svc/Dockerfile"
  fi
done
```

- [ ] **Step 2: Build one service to verify docker-compose works**

```bash
cd build/room-gatekeeper && docker compose build
```

- [ ] **Step 3: Final commit if any fixes needed**

```bash
git add -A && git commit -m "infra: finalize CI/CD and local dev infrastructure" || echo "nothing to commit"
```
